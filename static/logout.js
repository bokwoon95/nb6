const form = document.querySelector("form");
if (form && document.referrer) {
    const backLink = document.createElement("a");
    backLink.href = document.referrer;
    backLink.className = "linktext";
    backLink.innerHTML = "&larr; Go back";
    backLink.addEventListener("click", function(event) {
        history.back();
        event.preventDefault();
    });
    const div = document.createElement("div");
    div.appendChild(backLink);
    form.prepend(div);
}
