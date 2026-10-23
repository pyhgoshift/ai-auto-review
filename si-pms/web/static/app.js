// 확인 대화상자와 링크 복사. CSP 때문에 인라인 스크립트 대신 이 파일만 사용한다.
document.addEventListener('click', function (e) {
  var el = e.target.closest('[data-confirm]');
  if (el && !window.confirm(el.getAttribute('data-confirm'))) {
    e.preventDefault();
    return;
  }
  var copy = e.target.closest('[data-copy]');
  if (copy) {
    var input = document.getElementById(copy.getAttribute('data-copy'));
    input.select();
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(input.value);
    } else {
      document.execCommand('copy');
    }
    copy.textContent = '복사됨';
  }
});
document.addEventListener('click', function (e) {
  if (e.target.closest('[data-print]')) window.print();
});
