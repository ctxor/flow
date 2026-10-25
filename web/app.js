"use strict";
const $ = (s) => document.querySelector(s),
  esc = (s) =>
    String(s ?? "").replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
const state = {
  me: null,
  workspaces: [],
  wid: "",
  view: "overview",
  tasks: [],
  notes: [],
  events: [],
  reminders: [],
  notifications: [],
  members: [],
  bindings: [],
  deliveries: [],
  caps: {},
  source: null,
  register: false,
  editor: null,
};
const titles = {
  overview: ["我的一天", "整理眼前的事，留住转瞬的灵感。"],
  tasks: ["任务清单", "把每一个下一步，变得清晰可见。"],
  notes: ["随手备忘", "先记下来，让灵感慢慢生长。"],
  events: ["日程安排", "为重要的相遇，留好时间。"],
  collaboration: ["空间协作", "让彼此的进度，在同一个地方对齐。"],
  settings: ["平台接入", "在熟悉的对话里，随时记录与接收提醒。"],
};
function toast(text) {
  $("#toast").textContent = text;
  $("#toast").classList.remove("hidden");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => $("#toast").classList.add("hidden"), 4000);
}
async function api(path, options = {}) {
  const response = await fetch("/v1" + path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    credentials: "same-origin",
  });
  const data = await response.json();
  if (!response.ok) {
    if (response.status === 401 && state.me) showAuth();
    throw new Error(data.error || "暂时无法完成，请稍后重试");
  }
  return data;
}
const post = (path, data) =>
  api(path, { method: "POST", body: JSON.stringify(data) });
const when = (v) =>
  v
    ? new Date(v).toLocaleString("zh-CN", {
        timeZone: state.me?.timezone || "Asia/Shanghai",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })
    : "未设截止时间";
const empty = (text, symbol = "◌") =>
  `<div class="empty"><span>${symbol}</span>${esc(text)}</div>`;
function showAuth() {
  state.source?.close();
  state.source = null;
  state.me = null;
  state.tasks = [];
  state.notes = [];
  state.notifications = [];
  $("#chat-history").textContent = "";
  chat(
    "你好。可以让我创建任务、保存备忘、安排日程，或查询当前空间的任务。",
    "assistant",
  );
  $("#main-content").textContent = "";
  $("#app").classList.add("hidden");
  $("#auth-screen").classList.remove("hidden");
}
async function enter() {
  state.me = await api("/me");
  state.workspaces = await api("/workspaces");
  state.wid = state.workspaces[0]?.id || "";
  $("#auth-screen").classList.add("hidden");
  $("#app").classList.remove("hidden");
  $("#account-name").textContent = state.me.email;
  $("#timezone-name").textContent = state.me.timezone;
  $("#avatar").textContent = state.me.email[0].toUpperCase();
  $("#workspace-select").innerHTML = state.workspaces
    .map((w) => `<option value="${esc(w.id)}">${esc(w.name)}</option>`)
    .join("");
  $("#today-label").textContent = new Date().toLocaleDateString("zh-CN", {
    timeZone: state.me.timezone,
    year: "numeric",
    month: "long",
    day: "numeric",
    weekday: "long",
  });
  await refresh();
  connect();
}
async function refresh() {
  if (!state.wid || !state.me) return;
  const workspace = state.wid,
    uid = state.me.id;
  const suffix = "?workspace_id=" + encodeURIComponent(state.wid);
  const values = await Promise.all([
    api("/tasks" + suffix),
    api("/notes" + suffix),
    api("/events" + suffix),
    api("/reminders"),
    api("/notifications"),
    api("/workspaces/" + state.wid + "/members"),
    api("/bindings"),
    api("/deliveries"),
    api("/capabilities"),
  ]);
  if (workspace !== state.wid || uid !== state.me?.id) return;
  [
    state.tasks,
    state.notes,
    state.events,
    state.reminders,
    state.notifications,
    state.members,
    state.bindings,
    state.deliveries,
    state.caps,
  ] = values;
  render();
}
function connect() {
  state.source?.close();
  let cursor = state.notifications.at(-1)?.id || 0;
  const source = new EventSource("/v1/stream?after=" + cursor);
  state.source = source;
  source.onopen = () => {
    $("#live-status").textContent = "实时同步";
    $("#live-status").className = "live connected";
  };
  source.onerror = () => {
    $("#live-status").textContent = "正在重连";
    $("#live-status").className = "live";
  };
  source.addEventListener("notification", async (event) => {
    const n = JSON.parse(event.data);
    if (n.kind !== "workspace_changed") toast(n.body);
    try {
      await refresh();
    } catch {}
  });
}
function taskHTML(tasks) {
  if (!tasks.length) return empty("还没有任务。给今天安排一个小目标。", "☑");
  return tasks
    .map(
      (t) =>
        `<div class="task-row"><button class="check ${t.status === "done" ? "done" : ""}" data-action="toggle" data-id="${esc(t.id)}" aria-label="${t.status === "done" ? "重新打开" : "完成"}${esc(t.title)}">${t.status === "done" ? "✓" : ""}</button><div class="task-info"><div class="task-title ${t.status === "done" ? "done" : ""}">${esc(t.title)}</div><div class="task-meta"><span>${esc(when(t.due_at))}</span>${t.assignee_id ? `<span>指派给 ${esc(state.members.find((m) => m.user_id === t.assignee_id)?.email || "成员")}</span>` : ""}</div></div>${t.status === "open" && t.due_at && new Date(t.due_at) < new Date() ? '<span class="badge overdue">已逾期</span>' : ""}<div class="row-buttons"><button class="row-action" data-action="edit-task" data-id="${esc(t.id)}">编辑</button><button class="row-action" data-action="comments" data-id="${esc(t.id)}">评论</button></div></div>`,
    )
    .join("");
}
function notesHTML(notes) {
  if (!notes.length) return empty("突然想到的事，随时记在这里。", "▤");
  return `<div class="notes-grid">${notes.map((n) => `<article class="note"><h3>${esc(n.title)}</h3><p>${esc(n.content)}</p><div class="note-footer"><span>${n.visibility === "private" ? "仅自己可见" : "空间内共享"} · ${esc(when(n.created_at))}</span>${n.created_by === state.me.id ? `<button class="row-action" data-action="delete-note" data-id="${esc(n.id)}">删除</button>` : ""}</div></article>`).join("")}</div>`;
}
function eventsHTML(events) {
  if (!events.length) return empty("日程还是空白，留一点时间给自己。", "▦");
  return events
    .map((e) => {
      const day = new Intl.DateTimeFormat("zh-CN", {
        timeZone: state.me.timezone,
        day: "2-digit",
      })
        .format(new Date(e.starts_at))
        .replace("日", "");
      return `<div class="event-row"><div class="event-date">日程<strong>${esc(day)}</strong></div><div class="event-content"><h3>${esc(e.title)}</h3><p>${esc(when(e.starts_at))} — ${esc(when(e.ends_at))}</p></div><button class="row-action" data-action="delete-event" data-id="${esc(e.id)}">取消</button></div>`;
    })
    .join("");
}
function card(title, body, action = "", label = "查看全部") {
  return `<section class="card"><div class="card-heading"><h2>${title}</h2>${action ? `<button data-view="${action}">${label} ↗</button>` : ""}</div>${body}</section>`;
}
function render() {
  const open = state.tasks.filter((t) => t.status === "open"),
    done = state.tasks.length - open.length;
  $("#stats").innerHTML = [
    ["待完成任务", open.length, "项", "☑"],
    ["已完成", done, "项", "✓"],
    ["随手备忘", state.notes.length, "则", "▤"],
    [
      "未来日程",
      state.events.filter((e) => new Date(e.ends_at) > new Date()).length,
      "项",
      "▦",
    ],
  ]
    .map(
      ([label, n, unit, icon]) =>
        `<div class="stat"><span class="stat-label">${label}</span><div class="stat-value">${n}<small>${unit}</small></div><span class="stat-decoration">${icon}</span></div>`,
    )
    .join("");
  $("#view-title").textContent = titles[state.view][0];
  $("#view-description").textContent = titles[state.view][1];
  document
    .querySelectorAll("#navigation button")
    .forEach((b) =>
      b.classList.toggle("active", b.dataset.view === state.view),
    );
  $("#new-button").textContent =
    state.view === "notes"
      ? "＋ 新建备忘"
      : state.view === "events"
        ? "＋ 新建日程"
        : "＋ 新建任务";
  let content = "";
  if (state.view === "overview") {
    const reminders = state.reminders
      .filter((r) => r.workspace_id === state.wid && r.status === "pending")
      .slice(0, 5);
    content =
      card(
        `接下来要做的事 <span class="count">${open.length} 项待完成</span>`,
        taskHTML(open.slice(0, 5)),
        "tasks",
      ) +
      card("随手留住的灵感", notesHTML(state.notes.slice(0, 2)), "notes") +
      card(
        "即将到来的提醒",
        reminders.length
          ? reminders
              .map(
                (r) =>
                  `<div class="reminder-row"><span>${esc(state.tasks.find((t) => t.id === r.object_id)?.title || state.events.find((e) => e.id === r.object_id)?.title || "提醒")}<small class="muted"> · ${esc(when(r.fire_at))}</small></span><button class="row-action" data-action="cancel-reminder" data-id="${esc(r.id)}">取消</button></div>`,
              )
              .join("")
          : empty("设置好提醒，到时我们会通知你。"),
      );
  }
  if (state.view === "tasks")
    content = card(
      `全部任务 <span class="count">${state.tasks.length} 项</span>`,
      taskHTML(state.tasks),
    );
  if (state.view === "notes")
    content = card(
      "你的备忘",
      `<input id="note-search" class="search" placeholder="搜索标题或内容" aria-label="搜索备忘"><div id="notes-results">${notesHTML(state.notes)}</div>`,
    );
  if (state.view === "events")
    content = card("日程列表", eventsHTML(state.events));
  if (state.view === "collaboration")
    content = card(
      "空间成员",
      state.members
        .map(
          (m) =>
            `<div class="member-row"><span>${esc(m.email)}${m.user_id === state.me.id ? " <small>你</small>" : ""}</span><span class="badge">${{ owner: "所有者", editor: "可编辑", viewer: "只读" }[m.role]}</span></div>`,
        )
        .join("") +
        `<div class="row-buttons actions-space"><button class="primary small" data-action="invite">添加已注册成员</button><button class="secondary small" data-action="workspace">创建新空间</button></div>`,
    );
  if (state.view === "settings") {
    content =
      card(
        "连接你的消息入口",
        ["feishu", "wechat"]
          .map((p) => {
            const bound = state.bindings.some((b) => b.provider === p);
            return `<div class="setting-block"><h3>${p === "feishu" ? "飞书机器人" : "微信服务号"} <span class="badge">${bound ? "已绑定" : state.caps[p] ? "待绑定" : "暂未启用"}</span></h3><p>${p === "feishu" ? "在机器人私聊中记录想法，并接收任务提醒。" : "在服务号对话中记录任务；主动提醒取决于账号消息能力。"}</p><button class="${bound ? "secondary" : "primary"} small" data-action="${bound ? "unbind" : "bind"}" data-provider="${p}" ${!state.caps[p] && !bound ? "disabled" : ""}>${bound ? "解除绑定" : "生成绑定码"}</button><div id="code-${p}"></div></div>`;
          })
          .join(""),
      ) +
      card(
        "最近的消息投递",
        state.deliveries.length
          ? state.deliveries
              .slice(0, 12)
              .map(
                (d) =>
                  `<div class="delivery-row"><span>${d.provider === "feishu" ? "飞书" : "微信"}<small>${esc(when(d.created_at))}${d.last_error ? " · " + esc(d.last_error) : ""}</small></span><span class="badge">${{ queued: "等待发送", sending: "发送中", accepted: "平台已受理", retry: "等待重试", failed: "发送失败", unknown: "结果待核实", cancelled: "已取消" }[d.status] || esc(d.status)}</span></div>`,
              )
              .join("")
          : empty("绑定平台后，这里会显示投递结果。"),
      );
  }
  $("#main-content").innerHTML = content;
  const search = $("#note-search");
  if (search)
    search.addEventListener("input", () => {
      $("#notes-results").innerHTML = notesHTML(
        state.notes.filter((n) =>
          (n.title + n.content)
            .toLowerCase()
            .includes(search.value.toLowerCase()),
        ),
      );
    });
}
function field(label, name, type = "text", value = "", extra = "") {
  return `<label>${label}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
}
function openEditor(kind, task) {
  state.editor = { kind, task };
  $("#editor-error").textContent = "";
  let fields = "",
    title = "";
  if (kind === "task") {
    title = "新建任务";
    fields =
      field("任务标题", "title", "text", "", 'required maxlength="300"') +
      `<label>描述<textarea name="description" rows="2" maxlength="10000"></textarea></label>` +
      field("截止时间（本机时间，可选）", "due_at", "datetime-local") +
      field(
        "提前提醒（分钟，留空则不提醒）",
        "remind_before_minutes",
        "number",
        "",
        'min="0" max="525600"',
      ) +
      `<label>指派给<select name="assignee_id"><option value="">不指派</option>${state.members.map((m) => `<option value="${esc(m.user_id)}">${esc(m.email)}</option>`).join("")}</select></label>`;
  }
  if (kind === "edit-task") {
    title = "编辑任务";
    const local = task.due_at
      ? new Date(
          new Date(task.due_at).getTime() -
            new Date(task.due_at).getTimezoneOffset() * 60000,
        )
          .toISOString()
          .slice(0, 16)
      : "";
    fields =
      field(
        "任务标题",
        "title",
        "text",
        task.title,
        'required maxlength="300"',
      ) +
      field("截止时间（本机时间）", "due_at", "datetime-local", local) +
      `<p class="form-hint">调整截止时间时，已有提醒会同步改期。</p>`;
  }
  if (kind === "note") {
    title = "新建备忘";
    fields =
      field("标题", "title", "text", "", 'required maxlength="300"') +
      `<label>内容<textarea name="content" rows="5" maxlength="20000" required></textarea></label><label>谁可以看<select name="visibility"><option value="private">仅自己</option><option value="workspace">当前空间成员</option></select></label>`;
  }
  if (kind === "event") {
    title = "新建日程";
    fields =
      field("日程标题", "title", "text", "", 'required maxlength="300"') +
      field(
        "开始时间（本机时间）",
        "starts_at",
        "datetime-local",
        "",
        "required",
      ) +
      field(
        "结束时间（本机时间）",
        "ends_at",
        "datetime-local",
        "",
        "required",
      ) +
      field(
        "提前提醒（分钟，可选）",
        "remind_before_minutes",
        "number",
        "",
        'min="0" max="525600"',
      );
  }
  if (kind === "workspace") {
    title = "创建空间";
    fields = field("空间名称", "name", "text", "", 'required maxlength="100"');
  }
  if (kind === "invite") {
    title = "添加空间成员";
    fields =
      `<p class="form-hint">由空间所有者添加已注册用户。私有备忘仍仅作者可见。</p>` +
      field("成员邮箱", "email", "email", "", "required") +
      `<label>权限<select name="role"><option value="editor">可编辑</option><option value="viewer">只读</option></select></label>`;
  }
  $("#editor-title").textContent = title;
  $("#editor-fields").innerHTML = fields;
  $("#editor-save").classList.remove("hidden");
  $("#editor").showModal();
}
async function saveEditor(event) {
  event.preventDefault();
  const data = Object.fromEntries(new FormData(event.target)),
    kind = state.editor.kind;
  $("#editor-save").disabled = true;
  try {
    if (kind === "task" || kind === "event" || kind === "note") {
      data.workspace_id = state.wid;
      if (kind !== "note") {
        for (const k of ["due_at", "starts_at", "ends_at"]) {
          if (data[k]) data[k] = new Date(data[k]).toISOString();
          else delete data[k];
        }
        if (data.remind_before_minutes !== "")
          data.remind_before_minutes = Number(data.remind_before_minutes);
        else delete data.remind_before_minutes;
        if (!data.assignee_id) delete data.assignee_id;
      }
      await post(
        kind === "task" ? "/tasks" : kind === "event" ? "/events" : "/notes",
        data,
      );
    }
    if (kind === "edit-task") {
      const body = { title: data.title, version: state.editor.task.version };
      if (data.due_at) body.due_at = new Date(data.due_at).toISOString();
      await api("/tasks/" + state.editor.task.id, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
    }
    if (kind === "workspace") {
      const w = await post("/workspaces", data);
      state.workspaces = await api("/workspaces");
      state.wid = w.id;
      $("#workspace-select").innerHTML = state.workspaces
        .map((w) => `<option value="${esc(w.id)}">${esc(w.name)}</option>`)
        .join("");
      $("#workspace-select").value = w.id;
    }
    if (kind === "invite")
      await post("/workspaces/" + state.wid + "/members", data);
    if (kind === "comment")
      await post("/tasks/" + state.editor.task.id + "/comments", {
        body: data.body,
      });
    $("#editor").close();
    await refresh();
    toast("已保存");
  } catch (e) {
    $("#editor-error").textContent = e.message;
  } finally {
    $("#editor-save").disabled = false;
  }
}
async function comments(task) {
  const rows = await api("/tasks/" + task.id + "/comments");
  openEditor("comment", task);
  $("#editor-title").textContent = "任务评论";
  $("#editor-fields").innerHTML =
    `<h3>${esc(task.title)}</h3><div id="comment-list">${rows.length ? rows.map((c) => `<div class="comment-body"><small class="muted">${esc(state.members.find((m) => m.user_id === c.user_id)?.email || "成员")} · ${esc(when(c.created_at))}</small><p>${esc(c.body)}</p></div>`).join("") : empty("还没有评论")}</div><label>写一条评论<textarea name="body" required maxlength="2000" rows="3"></textarea></label>`;
}
document.addEventListener("click", async (event) => {
  const button = event.target.closest("button");
  if (!button) return;
  if (button.dataset.view) {
    state.view = button.dataset.view;
    render();
    return;
  }
  const a = button.dataset.action;
  if (!a) return;
  const id = button.dataset.id,
    task = state.tasks.find((t) => t.id === id);
  try {
    if (a === "toggle") {
      await api("/tasks/" + id, {
        method: "PATCH",
        body: JSON.stringify({
          status: task.status === "done" ? "open" : "done",
          version: task.version,
        }),
      });
      await refresh();
    }
    if (a === "edit-task") openEditor(a, task);
    if (a === "comments") await comments(task);
    if (a === "invite" || a === "workspace") openEditor(a);
    if (["delete-note", "delete-event", "cancel-reminder"].includes(a)) {
      if (
        !confirm(
          a === "cancel-reminder"
            ? "取消这条提醒？"
            : "确定删除或取消这项内容？",
        )
      )
        return;
      await api(
        (a === "delete-note"
          ? "/notes/"
          : a === "delete-event"
            ? "/events/"
            : "/reminders/") + id,
        { method: "DELETE" },
      );
      await refresh();
      toast("已取消或删除");
    }
    if (a === "bind") {
      const result = await post("/bindings/codes", {
        provider: button.dataset.provider,
      });
      $("#code-" + button.dataset.provider).innerHTML =
        `<div class="binding-code">在对应平台的私聊中发送：<br>${esc(result.command)}<br>10分钟内有效</div>`;
    }
    if (a === "unbind") {
      if (!confirm("解除这个平台的绑定？")) return;
      await api("/bindings/" + button.dataset.provider, { method: "DELETE" });
      await refresh();
    }
  } catch (e) {
    toast(e.message);
  }
});
$("#auth-toggle").addEventListener("click", () => {
  state.register = !state.register;
  $("#auth-title").textContent = state.register ? "创建你的空间" : "欢迎回来";
  $("#auth-description").textContent = state.register
    ? "从一件小事开始，让每一天更有序。"
    : "登录，继续安排你的每一天。";
  $("#auth-submit").textContent = state.register ? "创建账号" : "登录";
  $("#auth-toggle").textContent = state.register
    ? "已有账号？去登录"
    : "还没有账号？创建一个";
  $("#auth-error").textContent = "";
  $("#auth-form input[name=password]").autocomplete = state.register
    ? "new-password"
    : "current-password";
});
$("#auth-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  $("#auth-submit").disabled = true;
  $("#auth-error").textContent = "";
  try {
    const data = Object.fromEntries(new FormData(event.target));
    data.timezone =
      Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai";
    await post(state.register ? "/auth/register" : "/auth/login", data);
    await enter();
  } catch (e) {
    $("#auth-error").textContent = e.message;
  } finally {
    $("#auth-submit").disabled = false;
  }
});
$("#logout").addEventListener("click", async () => {
  try {
    await post("/auth/logout", {});
    showAuth();
  } catch (e) {
    toast(e.message);
  }
});
$("#workspace-select").addEventListener("change", async (event) => {
  state.wid = event.target.value;
  try {
    await refresh();
  } catch (e) {
    toast(e.message);
  }
});
$("#new-button").addEventListener("click", () =>
  openEditor(
    state.view === "notes"
      ? "note"
      : state.view === "events"
        ? "event"
        : "task",
  ),
);
$("#editor-form").addEventListener("submit", saveEditor);
$("#editor-close").addEventListener("click", () => $("#editor").close());
$("#editor-cancel").addEventListener("click", () => $("#editor").close());
document.querySelectorAll("[data-prompt]").forEach((b) =>
  b.addEventListener("click", () => {
    $("#assistant-input").value = b.dataset.prompt;
    $("#assistant-input").focus();
  }),
);
function chat(text, kind) {
  const node = document.createElement("div");
  node.className = "chat " + kind;
  node.textContent = text;
  $("#chat-history").append(node);
  $("#chat-history").scrollTop = $("#chat-history").scrollHeight;
  return node;
}
$("#assistant-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const text = $("#assistant-input").value.trim();
  if (!text) return;
  chat(text, "user");
  const node = chat("正在理解和处理…", "assistant pending");
  $("#assistant-input").value = "";
  $("#assistant-send").disabled = true;
  try {
    const job = await api("/assistant/messages", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ workspace_id: state.wid, text }),
    });
    const end = Date.now() + 65000;
    let result = job;
    while (["queued", "running"].includes(result.status) && Date.now() < end) {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      result = await api("/agent-jobs/" + job.id);
    }
    node.textContent = ["queued", "running"].includes(result.status)
      ? "仍在后台处理，完成后会通知你。"
      : result.result;
    node.classList.remove("pending");
    await refresh();
  } catch (e) {
    node.textContent = e.message;
    node.classList.remove("pending");
  } finally {
    $("#assistant-send").disabled = false;
  }
});
enter().catch(() => showAuth());
