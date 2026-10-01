// See console-redirect.html. The origin is fixed and only the path, query and
// fragment of THIS page are carried over, so nothing a visitor puts in the URL
// can send the browser anywhere but the console.
;(function () {
  var target = 'https://app.accesslink.store' + location.pathname + location.search + location.hash
  location.replace(target)
  document.addEventListener('DOMContentLoaded', function () {
    var link = document.getElementById('console-link')
    if (link) link.setAttribute('href', target)
  })
})()
