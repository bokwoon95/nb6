const element = document.querySelector("[data-go-back]");
if (element) {
    const a = document.createElement("a");
    a.href = document.referrer;
    a.className = "linktext";
    a.innerHTML = element.getAttribute("data-go-back") || "&larr; Go back";
    a.addEventListener("click", function(event) {
        history.back();
        event.preventDefault();
    });
    element.innerHTML = "";
    element.appendChild(a);
}
