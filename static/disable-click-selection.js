// https://stackoverflow.com/a/43321596
document.addEventListener('mousedown', function(event) {
    if (event.detail > 1) {
        event.preventDefault();
        // of course, you still do not know what you prevent here...
        // You could also check event.ctrlKey/event.shiftKey/event.altKey
        // to not prevent something useful.
    }
}, false);
