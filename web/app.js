const state = {
  messages: [],
  currentAssistantId: null,
  sending: false,
  config: null,
};

const el = (id) => document.getElementById(id);

// ===== Page switching =====

function showSettings() {
  el("settingsPage").style.display = "";
  el("chatPage").style.display = "none";
}

function showChat() {
  el("settingsPage").style.display = "none";
  el("chatPage").style.display = "";
  el("prompt").focus();
  if (state.config) {
    el("chatProviderLabel").textContent = state.config.provider === "openai" ? "OpenAI-compatible" : "Gemini";
  }
}

// ===== Config =====

function readConfigForm() {
  return {
    provider: el("provider").value,
    baseUrl: el("baseURL").value.trim(),
    apiKey: el("apiKey").value.trim(),
    model: el("model").value.trim(),
    workerUrl: el("workerURL").value.trim(),
    tunnel: {
    },
  };
}

function fillConfigForm(config) {
  el("provider").value = config.provider || "gemini";
  el("baseURL").value = config.baseUrl || "";
  el("apiKey").value = config.apiKey || "";
  el("model").value = config.model || "";
  el("workerURL").value = config.workerUrl || "";
}

async function loadConfig() {
  const response = await fetch("/api/config");
  const config = await response.json();
  state.config = config;
  fillConfigForm(config);
}

async function saveConfig() {
  const config = readConfigForm();
  const response = await fetch("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  if (!response.ok) {
    const text = await response.text();
    alert(`\u4fdd\u5b58\u5931\u8d25\uff1a${text}`);
    return;
  }
  state.config = config;
  const btn = el("saveBtn");
  btn.textContent = "\u5df2\u4fdd\u5b58";
  setTimeout(() => { btn.textContent = "\u4fdd\u5b58"; }, 1500);
}

// ===== Chat =====

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

function setSending(sending) {
  state.sending = sending;
  const btn = el("sendBtn");
  const textarea = el("prompt");
  if (sending) {
    btn.textContent = "\u53d1\u9001\u4e2d...";
    btn.disabled = true;
    btn.style.opacity = "0.6";
    textarea.disabled = true;
  } else {
    btn.textContent = "\u53d1\u9001";
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

  if (!state.config) {
    alert("\u8bf7\u5148\u4fdd\u5b58\u8bbe\u7f6e");
    return;
  }

  const provider = state.config.provider;
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
      updateMessage(assistantId, `\u9519\u8bef\uff1a${text}`);
      state.messages.push({ role: "assistant", content: `\u9519\u8bef\uff1a${text}` });
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
          assistantText = `\u9519\u8bef\uff1a${event.content || "unknown"}`;
          updateMessage(assistantId, assistantText);
        } else if (event.type === "done") {
          break;
        }
      }
    }
  } catch (err) {
    assistantText = `\u7f51\u7edc\u9519\u8bef\uff1a${err.message || String(err)}`;
    updateMessage(assistantId, assistantText);
  } finally {
    setSending(false);
    state.messages.push({ role: "assistant", content: assistantText || "(empty response)" });
  }
}

// ===== Event listeners =====

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
el("goChatBtn").addEventListener("click", () => {
  saveConfig().then(() => showChat()).catch((err) => alert(String(err)));
});
el("goSettingsBtn").addEventListener("click", showSettings);
el("sendBtn").addEventListener("click", () => sendMessage().catch((err) => {
  alert(String(err));
  setSending(false);
}));

// ===== Init =====

loadConfig().catch((err) => {
  console.error(err);
  alert(String(err));
});
