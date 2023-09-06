const urlSearchParams = (new URL(document.location)).searchParams;
const suffix = " Path=" + location.pathname + "; Max-Age=" + (60 * 60 * 24 * 365).toString() + "; SameSite=Lax;";
const deleteSuffix = " Path=" + location.pathname + "; Max-Age=-1; SameSite=Lax;";
let sort = urlSearchParams.get("sort");
if (sort) {
    sort = sort.trim().toLowerCase();
}
if (sort === "name" || sort === "created") {
    document.cookie = `sort=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (sort === "edited" || sort === "title") {
    document.cookie = `sort=${sort}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}
let order = urlSearchParams.get("order");
if (order) {
    order = order.trim().toLowerCase();
}
if ((order === "asc" && sort === "title") || (order === "desc" && (sort === "name" || sort === "created" || sort === "edited"))) {
    document.cookie = `order=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (order === "asc" || order === "desc") {
    document.cookie = `order=${order}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}
