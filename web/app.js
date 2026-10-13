const state = {
  config: null,
  messages: [],
  sending: false,
};

const el = (id) => document.getElementById(id);

function defaultProfile() {
  return {
    id: `profile-${Date.now()}`,
    name: "New Profile",
    provider: "gemini",
    baseUrl: "https://generativelanguage.googleapis.com/v1beta/openai",
    apiKey: "",
    model: "gemini-2.5-flash",
    routeMode: "direct",
    workerUrl: "",
    http3Enabled: false,
  };
}

function normalizeConfig(config) {
  const profiles = Array.isArray(config.profiles) && config.profiles.length > 0
    ? config.profiles
    : [defaultProfile()];
  const activeProfileId = config.activeProfileId || profiles[0].id;
  return { activeProfileId, profiles };
}

function activeProfile() {
  return state.config.profiles.find((profile) => profile.id === state.config.activeProfileId) || state.config.profiles[0];
}

function showSettings() {
  el("settingsPage").style.display = "";
  el("chatPage").style.display = "none";
}

function showChat() {
  el("settingsPage").style.display = "none";
  el("chatPage").style.display = "";
  const profile = activeProfile();
  el("chatProviderLabel").textContent = `${profile.name || profile.id} · ${profile.model}`;
  el("prompt").focus();
}

function renderProfileSelect() {
  const select = el("profileSelect");
  select.innerHTML = "";
  for (const profile of state.config.profiles) {
    const option = document.createElement("option");
    option.value = profile.id;
    option.textContent = `${profile.name || profile.id} (${profile.model || "no model"})`;
    select.appendChild(option);
  }
  select.value = state.config.activeProfileId;
}

function fillProfileForm(profile) {
  el("profileId").value = profile.id || "";
  el("profileName").value = profile.name || "";
  el("provider").value = profile.provider || "gemini";
  el("baseURL").value = profile.baseUrl || "";
  el("apiKey").value = profile.apiKey || "";
  el("model").value = profile.model || "";
  el("routeMode").value = profile.routeMode || "direct";
  el("workerURL").value = profile.workerUrl || "";
  el("quicEnable").checked = profile.http3Enabled === true;
}

function readProfileForm() {
  return {
    id: el("profileId").value.trim(),
    name: el("profileName").value.trim(),
    provider: el("provider").value,
    baseUrl: el("baseURL").value.trim(),
    apiKey: el("apiKey").value.trim(),
    model: el("model").value.trim(),
    routeMode: el("routeMode").value,
    workerUrl: el("workerURL").value.trim(),
    http3Enabled: el("quicEnable").checked,
  };
}

function syncFormToState() {
  const profile = readProfileForm();
  if (!profile.id) {
    profile.id = `profile-${Date.now()}`;
  }
  const index = state.config.profiles.findIndex((item) => item.id === state.config.activeProfileId);
  if (index >= 0) {
    state.config.profiles[index] = profile;
  }
  state.config.activeProfileId = profile.id;
}

async function loadConfig() {
  const response = await fetch("/api/config");
  const config = await response.json();
  state.config = normalizeConfig(config);
  renderProfileSelect();
  fillProfileForm(activeProfile());
}

async function saveConfig() {
  syncFormToState();
  const response = await fetch("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(state.config),
  });
  if (!response.ok) {
    const text = await response.text();
    alert(`保存失败：${text}`);
    return false;
  }
  renderProfileSelect();
  fillProfileForm(activeProfile());
  const btn = el("saveBtn");
  btn.textContent = "已保存";
  setTimeout(() => { btn.textContent = "保存"; }, 1500);
  return true;
}

function addProfile() {
  syncFormToState();
  const profile = defaultProfile();
  state.config.profiles.push(profile);
  state.config.activeProfileId = profile.id;
  renderProfileSelect();
  fillProfileForm(profile);
}

function deleteProfile() {
  if (state.config.profiles.length <= 1) {
    alert("至少保留一个 profile");
    return;
  }
  const id = state.config.activeProfileId;
  state.config.profiles = state.config.profiles.filter((profile) => profile.id !== id);
  state.config.activeProfileId = state.config.profiles[0].id;
  renderProfileSelect();
  fillProfileForm(activeProfile());
}

async function diagnoseProfile() {
  const saved = await saveConfig();
  if (!saved) return;
  const profile = activeProfile();
  const panel = el("diagnosePanel");
  const summary = el("diagnoseSummary");
  const table = el("diagnoseTable");
  panel.hidden = false;
  summary.textContent = "诊断中...";
  table.innerHTML = "";

  const response = await fetch(`/api/diagnose/profile/${encodeURIComponent(profile.id)}`, { method: "POST" });
  const result = await response.json();
  summary.textContent = `${result.profileId} · ${result.routeMode} · HTTP3=${result.http3Enabled} · ${result.errorClass || "ok"}`;
  const rows = [
    ["Status", result.statusCode || "-"],
    ["Error", result.error || "-"],
    ["Worker Health", result.workerHealth ? `${result.workerHealth.ok ? "ok" : "fail"} ${result.workerHealth.statusCode || ""} ${result.workerHealth.error || ""}` : "-"],
    ["Total", `${result.durationMs || 0}ms`],
    ["TCP Connect", `${result.traceMs?.tcpConnect ?? "-"}ms`],
    ["TLS", `${result.traceMs?.tlsHandshake ?? "-"}ms`],
    ["Wrote Request", `${result.traceMs?.wroteRequest ?? "-"}ms`],
    ["First Byte", `${result.traceMs?.firstByte ?? "-"}ms`],
    ["First Token", `${result.traceMs?.firstToken ?? "-"}ms`],
    ["Server Wait", `${result.traceMs?.serverWait ?? "-"}ms`],
  ];
  table.innerHTML = rows.map(([key, value]) => `<tr><th>${key}</th><td>${value}</td></tr>`).join("");
}

function appendMessage(role, content, id = crypto.randomUUID()) {
  const node = document.createElement("article");
  node.className = `message ${role}`;
  node.dataset.id = id;
  node.innerHTML = `<div class="role">${role === "user" ? "You" : "Assistant"}</div><div class="content"></div>`;
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
  el("sendBtn").disabled = sending;
  el("sendBtn").textContent = sending ? "发送中..." : "发送";
  el("prompt").disabled = sending;
}

async function sendMessage() {
  if (state.sending) return;
  const prompt = el("prompt").value.trim();
  if (!prompt) return;

  const profile = activeProfile();
  state.messages.push({ role: "user", content: prompt });
  appendMessage("user", prompt);
  el("prompt").value = "";
  setSending(true);

  const assistantId = crypto.randomUUID();
  appendMessage("assistant", "", assistantId);
  let assistantText = "";

  try {
    const response = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        profileId: profile.id,
        model: profile.model,
        messages: state.messages,
      }),
    });
    if (!response.ok || !response.body) {
      const text = await response.text();
      assistantText = `错误：${text}`;
      updateMessage(assistantId, assistantText);
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
        const event = JSON.parse(line);
        if (event.type === "delta" && event.content) {
          assistantText += event.content;
          updateMessage(assistantId, assistantText);
        }
        if (event.type === "error") {
          assistantText = `错误：${event.content || "unknown"}`;
          updateMessage(assistantId, assistantText);
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

el("profileSelect").addEventListener("change", () => {
  syncFormToState();
  state.config.activeProfileId = el("profileSelect").value;
  fillProfileForm(activeProfile());
});
el("newProfileBtn").addEventListener("click", addProfile);
el("deleteProfileBtn").addEventListener("click", deleteProfile);
el("saveBtn").addEventListener("click", () => saveConfig().catch((err) => alert(String(err))));
el("diagnoseBtn").addEventListener("click", () => diagnoseProfile().catch((err) => alert(String(err))));
el("goChatBtn").addEventListener("click", () => saveConfig().then((ok) => { if (ok) showChat(); }).catch((err) => alert(String(err))));
el("goSettingsBtn").addEventListener("click", showSettings);
el("sendBtn").addEventListener("click", () => sendMessage().catch((err) => alert(String(err))));
el("prompt").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    sendMessage().catch((err) => alert(String(err)));
  }
});

loadConfig().catch((err) => {
  console.error(err);
  alert(String(err));
});
