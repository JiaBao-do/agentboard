// Loads the Go WebAssembly UI. Kept external (not inline) so the page can run
// under a strict Content-Security-Policy.
(async () => {
  const boot = document.getElementById("boot");
  try {
    if (!WebAssembly.instantiateStreaming) {
      throw new Error("this browser lacks WebAssembly streaming support");
    }
    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(fetch("app.wasm"), go.importObject);
    go.run(result.instance);
  } catch (err) {
    if (boot) boot.textContent = "Could not start the UI: " + err;
    console.error(err);
  }
})();
