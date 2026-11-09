(() => {
  let scheme = "dark";
  try {
    const preference = localStorage.getItem("is-workbench-color-scheme");
    if (preference === "light" || preference === "dark") scheme = preference;
  } catch {}
  document.documentElement.dataset.mantineColorScheme = scheme;
  document.documentElement.style.colorScheme = scheme;
  document.documentElement.style.backgroundColor =
    scheme === "dark" ? "#0c1017" : "#f3f5f9";
})();
