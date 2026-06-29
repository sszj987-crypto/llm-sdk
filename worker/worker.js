export default {
  async fetch(request, env) {
    if (request.method === "OPTIONS") {
      return new Response(null, {
        status: 204,
        headers: corsHeaders(request),
      });
    }

    const url = new URL(request.url);
    if ((url.pathname === "/" || url.pathname === "/health") && request.method === "GET") {
      return new Response(JSON.stringify({ ok: true, service: "llm-agent-worker" }), {
        status: 200,
        headers: {
          "Content-Type": "application/json; charset=utf-8",
          ...corsHeaders(request),
        },
      });
    }

    const targetUrl = request.headers.get("X-Target-Url");
    if (!targetUrl) {
      return new Response("Missing Target", { status: 400, headers: corsHeaders(request) });
    }

    const targetHeaders = new Headers(request.headers);
    targetHeaders.delete("Host");
    targetHeaders.delete("X-Target-Url");
    targetHeaders.delete("Cf-Connecting-Ip");
    targetHeaders.delete("Cf-Ipcountry");
    targetHeaders.delete("Cf-Ray");
    targetHeaders.delete("Cf-Visitor");

    // First attempt: standard fetch()
    let upstream = await attemptFetch(targetUrl, targetHeaders, request.body);
    if (upstream) {
      const outHeaders = new Headers(upstream.headers);
      for (const [k, v] of Object.entries(corsHeaders(request))) {
        outHeaders.set(k, v);
      }
      return new Response(upstream.body, {
        status: upstream.status,
        headers: outHeaders,
      });
    }

    return new Response("Worker proxy error: all attempts failed", {
      status: 502,
      headers: corsHeaders(request),
    });
  },
};

async function attemptFetch(targetUrl, headers, body) {
  const proxied = new Request(targetUrl, {
    method: "POST",
    headers,
    body,
    redirect: "follow",
  });

  try {
    const response = await fetch(proxied);
    const clone = response.clone();

    // Check for region-lock errors from Gemini/OpenAI
    if (response.status === 400 || response.status === 403) {
      const text = await clone.text();
      if (
        text.includes("User location is not supported") ||
        text.includes("unsupported_country") ||
        text.includes("country_not_supported") ||
        text.includes("region_not_supported")
      ) {
        throw new Error("region_locked");
      }
      // Return the original error — region lock wasn't the cause
      return new Response(text, {
        status: response.status,
        headers: response.headers,
      });
    }

    return response;
  } catch (err) {
    if (err.message === "region_locked") {
      return attemptViaConnect(targetUrl, headers, body);
    }
    throw err;
  }
}

async function attemptViaConnect(targetUrl, headers, body) {
  const parsed = new URL(targetUrl);
  const hostname = parsed.hostname;
  const port = parsed.port || (parsed.protocol === "https:" ? 443 : 80);

  try {
    const tcpSocket = await connect({ hostname, port });
    const tlsOptions = { host: hostname, ALPNProtocols: ["h2"] };
    const tlsSocket = await startTls(tcpSocket, tlsOptions);

    const bodyBytes = body ? await readAll(body) : new Uint8Array(0);

    const headerLines = [
      `POST ${parsed.pathname}${parsed.search} HTTP/1.1`,
      `Host: ${hostname}`,
    ];
    for (const [k, v] of headers.entries()) {
      headerLines.push(`${k}: ${v}`);
    }
    headerLines.push(`Content-Length: ${bodyBytes.length}`);
    headerLines.push("Connection: close");
    headerLines.push("");
    headerLines.push("");

    const requestBytes = new TextEncoder().encode(
      headerLines.join("\r\n")
    );
    const fullRequest = concatUint8Arrays(requestBytes, bodyBytes);

    await writeAll(tlsSocket.writable, fullRequest);

    const reader = tlsSocket.readable.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let headersEnd = false;
    let statusCode = 0;
    let responseHeaders = new Headers();

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      if (!headersEnd) {
        const headerEndIdx = buffer.indexOf("\r\n\r\n");
        if (headerEndIdx >= 0) {
          const headerPart = buffer.slice(0, headerEndIdx);
          const bodyPart = buffer.slice(headerEndIdx + 4);
          buffer = bodyPart;
          headersEnd = true;

          const lines = headerPart.split("\r\n");
          const statusLine = lines[0];
          const statusMatch = statusLine.match(/^HTTP\/\d\.\d (\d+)/);
          statusCode = statusMatch ? parseInt(statusMatch[1]) : 502;

          for (let i = 1; i < lines.length; i++) {
            const colonIdx = lines[i].indexOf(":");
            if (colonIdx > 0) {
              const key = lines[i].slice(0, colonIdx).trim();
              const val = lines[i].slice(colonIdx + 1).trim();
              responseHeaders.set(key, val);
            }
          }
        }
      }
    }

    const bodyStr = buffer;
    return new Response(bodyStr, {
      status: statusCode || 502,
      headers: responseHeaders,
    });
  } catch (err) {
    return null;
  }
}

function concatUint8Arrays(a, b) {
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

async function readAll(body) {
  const reader = body.getReader();
  const chunks = [];
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    chunks.push(value);
  }
  const totalLength = chunks.reduce((sum, c) => sum + c.length, 0);
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const c of chunks) {
    out.set(c, offset);
    offset += c.length;
  }
  return out;
}

async function writeAll(writable, data) {
  const writer = writable.getWriter();
  await writer.write(data);
  await writer.close();
}

function corsHeaders(request) {
  const origin = request.headers.get("Origin") || "*";
  return {
    "Access-Control-Allow-Origin": origin,
    "Access-Control-Allow-Methods": "GET,POST,OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type, Authorization, X-Target-Url, x-goog-api-key",
  };
}
