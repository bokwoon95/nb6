const element = document.querySelector("[data-go-back]");
if (element && element.tagName == "A") {
    element.setAttribute("href", document.referrer);
    element.setAttribute("onclick", "history.back(); return false;");
}
