// Copy-to-clipboard: <button data-copy="#selector">; the button text becomes "Copied" briefly.
document.addEventListener("click", function (e) {
  var btn = e.target.closest("[data-copy]");
  if (!btn) return;
  var src = document.querySelector(btn.getAttribute("data-copy"));
  if (!src || !navigator.clipboard) return;
  navigator.clipboard.writeText(src.value || src.textContent).then(function () {
    var label = btn.querySelector("[data-copy-label]");
    if (!label) return;
    var prev = label.textContent;
    label.textContent = "Copied";
    setTimeout(function () { label.textContent = prev; }, 1500);
  });
});
