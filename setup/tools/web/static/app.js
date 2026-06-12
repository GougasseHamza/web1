const refreshButton = document.querySelector("#refresh");
const filesTable = document.querySelector("#files");
const uploadForm = document.querySelector("#upload-form");
const uploadResult = document.querySelector("#upload-result");

async function refreshFiles() {
  refreshButton.disabled = true;
  try {
    const response = await fetch("/api/files", { headers: { "Accept": "application/json" } });
    const files = await response.json();
    filesTable.innerHTML = files.map((file) => `
      <tr>
        <td>${file.name}</td>
        <td>${file.owner}</td>
        <td>${file.updated}</td>
        <td><code>${file.path}</code></td>
      </tr>
    `).join("");
  } finally {
    refreshButton.disabled = false;
  }
}

refreshButton?.addEventListener("click", refreshFiles);

uploadForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const submit = uploadForm.querySelector("button");
  submit.disabled = true;
  uploadResult.className = "upload-result";
  uploadResult.textContent = "";

  try {
    const response = await fetch("/upload", {
      method: "POST",
      body: new FormData(uploadForm),
      headers: { "Accept": "application/json" },
    });
    const payload = await response.json();
    uploadResult.classList.add(response.ok ? "success" : "warning");
    uploadResult.textContent = payload.message || payload.notice || payload.reason || payload.error || "Upload processed.";
  } catch {
    uploadResult.classList.add("warning");
    uploadResult.textContent = "Upload queue is temporarily unavailable.";
  } finally {
    submit.disabled = false;
  }
});
