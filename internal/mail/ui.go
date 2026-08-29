package mail

// indexHTML, posta kutusu arayüzü. Tek dosya, dış kaynak yok — DevBox
// çevrimdışı da çalışmalı.
//
// %s: SMTP adresi.
const indexHTML = `<!doctype html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DevBox — Posta Kutusu</title>
<link rel="icon" href="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAM0lEQVR42mNgGD5ANfn1f1IwVgNIsQynC4h1KU5TcSlAFifKC/j8TLswGHgDKEoHIxgAABJedWHR4tT9AAAAAElFTkSuQmCC">
<style>
  :root {
    color-scheme: light dark;
    --kenar: #d6d9e0; --zemin: #fff; --ikincil: #f6f7f9;
    --metin: #1a1d23; --soluk: #6b7280; --vurgu: #2563eb;
  }
  @media (prefers-color-scheme: dark) {
    :root { --kenar:#2c313a; --zemin:#15181d; --ikincil:#1b1f26;
            --metin:#e5e7eb; --soluk:#9ca3af; --vurgu:#60a5fa; }
  }
  * { box-sizing: border-box; }
  body { margin:0; font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;
         color:var(--metin); background:var(--zemin); }
  header { display:flex; align-items:center; gap:12px; padding:10px 16px;
           border-bottom:1px solid var(--kenar); background:var(--ikincil); }
  header h1 { font-size:15px; margin:0; font-weight:600; }
  header .smtp { color:var(--soluk); font-size:12px; }
  header .sag { margin-left:auto; display:flex; gap:8px; align-items:center; }
  button { font:inherit; padding:4px 10px; border:1px solid var(--kenar);
           border-radius:6px; background:var(--zemin); color:var(--metin); cursor:pointer; }
  button:hover { border-color:var(--vurgu); }
  main { display:grid; grid-template-columns:340px 1fr; height:calc(100vh - 49px); }
  #liste { overflow-y:auto; border-right:1px solid var(--kenar); }
  .satir { padding:10px 14px; border-bottom:1px solid var(--kenar); cursor:pointer; }
  .satir:hover { background:var(--ikincil); }
  .satir.secili { background:var(--ikincil); box-shadow:inset 3px 0 0 var(--vurgu); }
  .satir .konu { font-weight:600; margin-bottom:2px; }
  .satir .kimden, .satir .zaman { color:var(--soluk); font-size:12px; }
  .not { color:var(--soluk); font-size:12px; margin:8px 0 0; }
  #ara { font:inherit; padding:4px 8px; width:220px; border-radius:6px;
         border:1px solid var(--kenar); background:var(--zemin); color:var(--metin); }
  #ara:focus { outline:none; border-color:var(--vurgu); }
  .rozet { display:inline-block; font-size:11px; color:var(--soluk);
           border:1px solid var(--kenar); border-radius:4px; padding:0 4px; margin-left:4px; }
  #detay { overflow-y:auto; padding:16px 20px; }
  #detay h2 { font-size:17px; margin:0 0 10px; }
  .basliklar { color:var(--soluk); font-size:13px; margin-bottom:14px; }
  .sekmeler { display:flex; gap:6px; margin-bottom:12px; }
  .sekmeler button.etkin { border-color:var(--vurgu); color:var(--vurgu); }
  pre { white-space:pre-wrap; word-break:break-word; background:var(--ikincil);
        padding:12px; border-radius:6px; margin:0; }
  iframe { width:100%%; height:60vh; border:1px solid var(--kenar); border-radius:6px; background:#fff; }
  .bos { color:var(--soluk); padding:40px 20px; text-align:center; }
  ul.ekler { list-style:none; padding:0; margin:12px 0 0; }
  ul.ekler li { padding:6px 0; border-top:1px solid var(--kenar); }
</style>
</head>
<body>
<header>
  <h1>DevBox Posta Kutusu</h1>
  <span class="smtp">SMTP: %s</span>
  <span class="sag">
    <input id="ara" type="search" placeholder="Ara: konu, adres, gövde" autocomplete="off">
    <span id="durum" class="smtp"></span>
    <button id="temizle">Tümünü sil</button>
  </span>
</header>
<main>
  <div id="liste"><div class="bos">Henüz posta yok.</div></div>
  <div id="detay"><div class="bos">Soldan bir posta seçin.</div></div>
</main>
<script>
(function () {
  var liste = document.getElementById('liste');
  var detay = document.getElementById('detay');
  var durum = document.getElementById('durum');
  var secili = null;

  function kacar(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c];
    });
  }

  function zaman(iso) {
    try { return new Date(iso).toLocaleTimeString('tr-TR'); } catch (e) { return ''; }
  }

  function listeyiCiz(mesajlar) {
    if (!mesajlar.length) {
      var bos = document.getElementById('ara').value
        ? 'Aramaya uyan posta yok.' : 'Henüz posta yok.';
      liste.innerHTML = '<div class="bos">' + bos + '</div>';
      return;
    }
    liste.innerHTML = mesajlar.map(function (m) {
      var rozetler = '';
      if (m.attachmentCount) rozetler += '<span class="rozet">' + m.attachmentCount + ' ek</span>';
      if (m.hasHtml) rozetler += '<span class="rozet">HTML</span>';
      return '<div class="satir" data-id="' + kacar(m.id) + '">' +
        '<div class="konu">' + (kacar(m.subject) || '(konu yok)') + rozetler + '</div>' +
        '<div class="kimden">' + kacar(m.from) + ' → ' + kacar((m.to || []).join(', ')) + '</div>' +
        '<div class="zaman">' + zaman(m.received) + '</div></div>';
    }).join('');
    if (secili) isaretle(secili);
  }

  function isaretle(id) {
    Array.prototype.forEach.call(liste.querySelectorAll('.satir'), function (el) {
      el.classList.toggle('secili', el.dataset.id === id);
    });
  }

  function detayCiz(m) {
    var sekmeler = [];
    if (m.text) sekmeler.push('metin');
    if (m.html) sekmeler.push('html');
    sekmeler.push('ham');

    var basliklar = '<div class="basliklar">' +
      '<div><b>Kimden:</b> ' + kacar(m.from) + '</div>' +
      '<div><b>Kime:</b> ' + kacar((m.to || []).join(', ')) + '</div>' +
      '<div><b>Zaman:</b> ' + kacar(new Date(m.received).toLocaleString('tr-TR')) + '</div>' +
      '</div>';

    var ekler = '';
    if (m.attachments && m.attachments.length) {
      ekler = '<ul class="ekler">' + m.attachments.map(function (a, i) {
        return '<li><a href="/api/messages/' + encodeURIComponent(m.id) +
          '/attachments/' + i + '" download>' + kacar(a.filename) + '</a> ' +
          '<span class="smtp">' + kacar(a.contentType) + ', ' + a.size + ' bayt</span></li>';
      }).join('') + '</ul>';
    }

    detay.innerHTML = '<h2>' + (kacar(m.subject) || '(konu yok)') + '</h2>' + basliklar +
      '<div class="sekmeler">' + sekmeler.map(function (s) {
        return '<button data-sekme="' + s + '">' + s + '</button>';
      }).join('') + '</div><div id="govde"></div>' + ekler;

    var govde = document.getElementById('govde');

    function goster(hangi) {
      Array.prototype.forEach.call(detay.querySelectorAll('.sekmeler button'), function (b) {
        b.classList.toggle('etkin', b.dataset.sekme === hangi);
      });
      if (hangi === 'html') {
        // Yakalanan HTML güvenilmez: uygulamanın gönderdiği içerikte
        // kullanıcıdan gelen veri olabilir. Kendi ilkesini taşıyan ayrı bir
        // uç noktadan, sandbox'lı çerçevede gösteriyoruz — aksi hâlde postayı
        // tetikleyen kişi bu arayüzde betik çalıştırabilirdi.
        var f = document.createElement('iframe');
        f.setAttribute('sandbox', '');
        f.setAttribute('src', '/api/messages/' + encodeURIComponent(m.id) + '/html');
        govde.innerHTML = '';
        govde.appendChild(f);
        govde.insertAdjacentHTML('beforeend',
          '<p class="not">Uzak içerik (resim, betik) engellendi.</p>');
      } else if (hangi === 'metin') {
        govde.innerHTML = '<pre>' + kacar(m.text) + '</pre>';
      } else {
        govde.innerHTML = '<pre id="hamgovde">yükleniyor…</pre>';
        fetch('/api/messages/' + encodeURIComponent(m.id) + '/raw')
          .then(function (r) { return r.text(); })
          .then(function (t) { document.getElementById('hamgovde').textContent = t; });
      }
    }

    Array.prototype.forEach.call(detay.querySelectorAll('.sekmeler button'), function (b) {
      b.addEventListener('click', function () { goster(b.dataset.sekme); });
    });
    goster(sekmeler[0]);
  }

  function yenile() {
    var q = document.getElementById('ara').value;
    fetch('/api/messages?q=' + encodeURIComponent(q))
      .then(function (r) { return r.json(); })
      .then(listeyiCiz);
  }

  // Arama kutusu her tuşta istek atmasın; yazma bitince bir kez sorsun.
  var araZaman;
  document.getElementById('ara').addEventListener('input', function () {
    clearTimeout(araZaman);
    araZaman = setTimeout(yenile, 150);
  });

  liste.addEventListener('click', function (e) {
    var satir = e.target.closest('.satir');
    if (!satir) return;
    secili = satir.dataset.id;
    isaretle(secili);
    fetch('/api/messages/' + encodeURIComponent(secili))
      .then(function (r) { return r.json(); })
      .then(detayCiz);
  });

  document.getElementById('temizle').addEventListener('click', function () {
    fetch('/api/messages', { method: 'DELETE' }).then(function () {
      secili = null;
      detay.innerHTML = '<div class="bos">Soldan bir posta seçin.</div>';
      yenile();
    });
  });

  var akis = new EventSource('/api/stream');
  akis.onopen = function () { durum.textContent = 'canlı'; };
  akis.onerror = function () { durum.textContent = 'bağlantı koptu'; };
  akis.onmessage = function () { yenile(); };

  yenile();
})();
</script>
</body>
</html>
`
