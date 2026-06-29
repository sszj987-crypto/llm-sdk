const state = {
  messages: [],
  currentAssistantId: null,
  sending: false,
};

const el = (id) => document.getElementById(id);

function appendMessage(role, content, id = crypto.randomUUID()) {
  const node = document.createElement("article");
  node.className = `message ${role}`;
  node.dataset.id = id;
  node.innerHTML = `<div class="role">${role === "user" ? "You" : role === "assistant" ? "Assistant" : role}</div><div class="content"></div>`;
  node.querySelector(".content").textContent = content;
  el("messages").appendChild(node);
  el("messages").scrollTop = el("messages").scrollHeight;
  return node;
}

function updateMessage(id, content) {
  const node = el("messages").querySelector(`[data-id="${id}"]`);
  if (!node) return;
  node.querySelector(".content").textContent = content;
  el("messages").scrollTop = el("messages").scrollHeight;
}

function readConfigForm() {
  return {
    openai: {
      baseUrl: el("openaiBaseURL").value.trim(),
      apiKey: el("openaiAPIKey").value.trim(),
      model: el("openaiModel").value.trim(),
      workerUrl: el("openaiWorkerURL").value.trim(),
    },
    gemini: {
      baseUrl: el("geminiBaseURL").value.trim(),
      apiKey: el("geminiAPIKey").value.trim(),
      model: el("geminiModel").value.trim(),
      workerUrl: el("geminiWorkerURL").value.trim(),
    },
    tunnel: {
      enableEch: el("enableEch").checked,
    },
  };
}

function fillConfigForm(config) {
  el("openaiBaseURL").value = config.openai?.baseUrl ?? "";
  el("openaiAPIKey").value = config.openai?.apiKey ?? "";
  el("openaiModel").value = config.openai?.model ?? "";
  el("openaiWorkerURL").value = config.openai?.workerUrl ?? "";

  el("geminiBaseURL").value = config.gemini?.baseUrl ?? "";
  el("geminiAPIKey").value = config.gemini?.apiKey ?? "";
  el("geminiModel").value = config.gemini?.model ?? "";
  el("geminiWorkerURL").value = config.gemini?.workerUrl ?? "";

  el("enableEch").checked = config.tunnel?.enableEch ?? false;
}

async function loadConfig() {
  const response = await fetch("/api/config");
  const config = await response.json();
  fillConfigForm(config);
}

async function saveConfig() {
  const response = await fetch("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(readConfigForm()),
  });
  if (!response.ok) {
    const text = await response.text();
    alert(`保存失败：${text}`);
    return;
  }
  const btn = el("saveBtn");
  btn.textContent = "已保存";
  setTimeout(() => { btn.textContent = "保存配置"; }, 1500);
}

function setSending(sending) {
  state.sending = sending;
  const btn = el("sendBtn");
  const textarea = el("prompt");
  if (sending) {
    btn.textContent = "发送中...";
    btn.disabled = true;
    btn.style.opacity = "0.6";
    textarea.disabled = true;
  } else {
    btn.textContent = "发送";
    btn.disabled = false;
    btn.style.opacity = "1";
    textarea.disabled = false;
    textarea.focus();
  }
}

async function sendMessage() {
  if (state.sending) return;

  const prompt = el("prompt").value.trim();
  if (!prompt) return;

  const provider = el("chatProvider").value;
  state.messages.push({ role: "user", content: prompt });
  appendMessage("user", prompt);
  el("prompt").value = "";
  setSending(true);

  const assistantId = crypto.randomUUID();
  state.currentAssistantId = assistantId;
  appendMessage("assistant", "", assistantId);

  let assistantText = "";

  try {
    const response = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider,
        messages: state.messages,
      }),
    });

    if (!response.ok || !response.body) {
      const text = await response.text();
      updateMessage(assistantId, `错误：${text}`);
      state.messages.push({ role: "assistant", content: `错误：${text}` });
      return;
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let idx;
      while ((idx = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, idx).trim();
        buffer = buffer.slice(idx + 1);
        if (!line) continue;

        let event;
        try {
          event = JSON.parse(line);
        } catch {
          continue;
        }

        if (event.type === "delta" && event.content) {
          assistantText += event.content;
          updateMessage(assistantId, assistantText);
        } else if (event.type === "error") {
          assistantText = `错误：${event.content || "unknown"}`;
          updateMessage(assistantId, assistantText);
        } else if (event.type === "done") {
          break;
        }
      }
    }
  } catch (err) {
    assistantText = `网络错误：${err.message || String(err)}`;
    updateMessage(assistantId, assistantText);
  } finally {
    setSending(false);
    state.messages.push({ role: "assistant", content: assistantText || "(empty response)" });
  }
}

// Enter to send, Shift+Enter for newline
el("prompt").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    sendMessage().catch((err) => {
      alert(String(err));
      setSending(false);
    });
  }
});

el("saveBtn").addEventListener("click", () => saveConfig().catch((err) => alert(String(err))));
el("sendBtn").addEventListener("click", () => sendMessage().catch((err) => {
  alert(String(err));
  setSending(false);
}));

loadConfig().catch((err) => {
  console.error(err);
  alert(String(err));
});
