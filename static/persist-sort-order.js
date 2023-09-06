const urlSearchParams = (new URL(document.location)).searchParams;
const suffix = " Path=" + location.pathname + "; Max-Age=" + (60 * 60 * 24 * 365).toString() + "; SameSite=Lax;";
if (urlSearchParams.has("sort")) {
    const sort = urlSearchParams.get("sort").trim().toLowerCase();
    switch (sort) {
        case "created":
            document.cookie = "sort=created;" + suffix;
            break;
        case "edited":
            document.cookie = "sort=edited;" + suffix;
            break;
        case "title":
            document.cookie = "sort=title;" + suffix;
            break;
    }
}
if (urlSearchParams.has("order")) {
    const order = urlSearchParams.get("order").trim().toLowerCase();
    switch (order) {
        case "asc":
            document.cookie = "order=asc;" + suffix;
            break;
        case "desc":
            document.cookie = "order=desc;" + suffix;
            break;
    }
}
