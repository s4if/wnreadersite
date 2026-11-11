// Reader keyboard navigation: arrow keys move between chapters.
(function () {
  document.addEventListener('keydown', function (e) {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    var target = e.target;
    if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable)) return;

    var rel = null;
    if (e.key === 'ArrowLeft') rel = 'prev';
    else if (e.key === 'ArrowRight') rel = 'next';
    else return;

    var link = document.querySelector('a[rel="' + rel + '"]');
    if (link) window.location.href = link.href;
  });
})();
