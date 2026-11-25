document.addEventListener("DOMContentLoaded", () => {
  const get = (name) => document.getElementById(`market-contract-settings${name ? `-${name}` : ""}`);
  const dialog = get();
  if (!dialog) return;

  const open = get("open");
  const rows = get("rows");
  const preset = get("preset");
  const add = get("add");
  const save = get("save");
  const error = get("error");
  const initial = JSON.parse(get("links").textContent);
  const maxLinks = 4;

  function updateActions() {
    preset.disabled = add.disabled = rows.children.length >= maxLinks;
  }

  function appendRow(link = {}, focus = false) {
    if (rows.children.length >= maxLinks) return;
    const row = get("row").content.firstElementChild.cloneNode(true);
    const label = row.querySelector('[name="label"]');
    label.value = link.label || "";
    row.querySelector('[name="url"]').value = link.url || "";
    row.querySelector("button").addEventListener("click", () => {
      const next = row.nextElementSibling || row.previousElementSibling;
      row.remove();
      updateActions();
      (next?.querySelector('[name="label"]') || add).focus();
    });
    rows.append(row);
    updateActions();
    if (focus) label.focus();
  }

  open.addEventListener("click", () => {
    error.hidden = true;
    rows.replaceChildren();
    for (const link of initial) appendRow(link);
    updateActions();
    dialog.showModal();
  });
  preset.addEventListener("change", () => {
    if (!preset.value) return;
    const link = { label: preset.selectedOptions[0].textContent, url: preset.value };
    preset.value = "";
    appendRow(link, true);
  });
  add.addEventListener("click", () => appendRow({}, true));
  get("cancel").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => open.focus());

  get("form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (save.disabled) return;
    error.hidden = true;
    const source_links = Array.from(rows.children, (row) => ({
      label: row.querySelector('[name="label"]').value.trim(),
      url: row.querySelector('[name="url"]').value.trim(),
    }));
    if (source_links.some(({ label }) => !label || [...label].length > 40)) {
      error.textContent = "合约数据来源名称须为 1 至 40 字。";
      error.hidden = false;
      return;
    }

    save.disabled = true;
    try {
      const response = await window.researchFetch(`${window.RESEARCH_PREFIX || ""}/api/sn/market/contracts/config`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source_links }),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => null);
        throw new Error(typeof payload?.detail === "string" ? payload.detail : "合约数据来源未保存，请重试。");
      }
      window.location.reload();
    } catch (failure) {
      error.textContent = failure.message || "合约数据来源未保存，请重试。";
      error.hidden = false;
    } finally {
      save.disabled = false;
    }
  });
});
