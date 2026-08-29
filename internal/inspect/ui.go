package inspect

// indexHTML, HTTP denetleyicisi arayüzü. Tek dosya, dış kaynak yok.
const indexHTML = `<!doctype html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DevBox — HTTP Denetleyici</title>
<link rel="icon" href="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAM0lEQVR42mNgGD5ANfn1f1IwVgNIsQynC4h1KU5TcSlAFifKC/j8TLswGHgDKEoHIxgAABJedWHR4tT9AAAAAElFTkSuQmCC">
<style>
  :root {
    color-scheme: light dark;
    --kenar:#d6d9e0; --zemin:#fff; --ikincil:#f6f7f9;
    --metin:#1a1d23; --soluk:#6b7280; --vurgu:#2563eb;
    --iyi:#16a34a; --kotu:#dc2626; --uyari:#d97706;
  }
  @media (prefers-color-scheme: dark) {
    :root { --kenar:#2c313a; --zemin:#15181d; --ikincil:#1b1f26;
            --metin:#e5e7eb; --soluk:#9ca3af; --vurgu:#60a5fa;
            --iyi:#4ade80; --kotu:#f87171; --uyari:#fbbf24; }
  }
  * { box-sizing:border-box; }
  body { margin:0; font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;
         color:var(--metin); background:var(--zemin); }
  header { display:flex; align-items:center; gap:12px; padding:10px 16px;
           border-bottom:1px solid var(--kenar); background:var(--ikincil); flex-wrap:wrap; }
  header h1 { font-size:15px; margin:0; font-weight:600; }
  header .sag { margin-left:auto; display:flex; gap:8px; align-items:center; }
  button { font:inherit; padding:4px 10px; border:1px solid var(--kenar);
           border-radius:6px; background:var(--zemin); color:var(--metin); cursor:pointer; }
  button:hover:not(:disabled) { border-color:var(--vurgu); }
  button:disabled { opacity:.5; cursor:default; }
  button.acik { border-color:var(--iyi); color:var(--iyi); }
  input[type=search] { font:inherit; padding:4px 8px; width:220px; border-radius:6px;
         border:1px solid var(--kenar); background:var(--zemin); color:var(--metin); }
  main { display:grid; grid-template-columns:minmax(340px,420px) 1fr; height:calc(100vh - 51px); }
  #liste { overflow-y:auto; border-right:1px solid var(--kenar); }
  .satir { padding:8px 12px; border-bottom:1px solid var(--kenar); cursor:pointer;
           display:grid; grid-template-columns:auto 1fr auto; gap:8px; align-items:baseline; }
  .satir:hover { background:var(--ikincil); }
  .satir.secili { background:var(--ikincil); box-shadow:inset 3px 0 0 var(--vurgu); }
  .yontem { font-weight:600; font-size:12px; font-family:ui-monospace,monospace; }
  .yol { overflow:hidden; text-overflow:ellipsis; white-space:nowrap;
         font-family:ui-monospace,monospace; font-size:12px; }
  .durum { font-size:12px; font-variant-numeric:tabular-nums; }
  .d2 { color:var(--iyi); } .d3 { color:var(--vurgu); }
  .d4 { color:var(--uyari); } .d5 { color:var(--kotu); }
  .satir .alt { grid-column:1 / -1; color:var(--soluk); font-size:11px; }
  #detay { overflow:auto; padding:0 0 24px; }
  .baslik { padding:10px 16px; border-bottom:1px solid var(--kenar);
            display:flex; gap:10px; align-items:center; flex-wrap:wrap;
            position:sticky; top:0; background:var(--zemin); }
  .bolum { padding:12px 16px; border-bottom:1px solid var(--kenar); }
  .bolum h3 { margin:0 0 8px; font-size:12px; text-transform:uppercase;
              letter-spacing:.04em; color:var(--soluk); }
  table.basliklar { width:100%; border-collapse:collapse; font-size:12px;
                    font-family:ui-monospace,monospace; }
  table.basliklar td { padding:2px 8px 2px 0; vertical-align:top; }
  table.basliklar td:first-child { color:var(--soluk); white-space:nowrap; }
  pre { margin:0; padding:10px; background:var(--ikincil); border-radius:6px;
        font:12px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace;
        white-space:pre-wrap; word-break:break-word; max-height:420px; overflow:auto; }
  .bos { padding:24px 16px; color:var(--soluk); }
  .rozet { font-size:11px; padding:1px 6px; border:1px solid var(--kenar);
           border-radius:999px; color:var(--soluk); }
  .uyari { color:var(--uyari); font-size:12px; margin-top:6px; }
</style>
</head>
<body>
<header>
  <h1>HTTP Denetleyici</h1>
  <span class="rozet" id="alan"></span>
  <span class="sag">
    <input id="ara" type="search" placeholder="Ara: yol, konak, gövde" autocomplete="off">
    <button id="kayit">Kayıt: —</button>
    <button id="temizle">Temizle</button>
  </span>
</header>
<main>
  <div id="liste"><div class="bos">Bekleniyor…</div></div>
  <div id="detay"><div class="bos">Soldan bir istek seçin.</div></div>
</main>
<script>
(function () {
  var liste = document.getElementById('liste');
  var detay = document.getElementById('detay');
  var kayitDugme = document.getElementById('kayit');
  var ara = document.getElementById('ara');
  var secili = null;
  var kayitlar = [];

  function kacar(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c];
    });
  }

  function boyut(b) {
    if (!b) return '0 B';
    var u = ['B','KB','MB'], i = 0, d = b;
    while (d >= 1024 && i < u.length - 1) { d /= 1024; i++; }
    return (d >= 10 || i === 0 ? Math.round(d) : d.toFixed(1)) + ' ' + u[i];
  }

  function zaman(iso) {
    var d = new Date(iso);
    return d.toLocaleTimeString('tr-TR') + '.' +
      String(d.getMilliseconds()).padStart(3, '0');
  }

  function durumSinifi(s) { return 'd' + String(s).charAt(0); }

  function suz(kayit) {
    var q = ara.value.toLowerCase();
    if (!q) return true;
    return (kayit.path + ' ' + kayit.host + ' ' + kayit.method).toLowerCase().indexOf(q) >= 0;
  }

  function listeyiCiz() {
    var gosterilecek = kayitlar.filter(suz);
    if (!gosterilecek.length) {
      liste.innerHTML = '<div class="bos">' +
        (ara.value ? 'Aramaya uyan istek yok.' :
         'Henüz istek geçmedi. Sitenizi tarayıcıda açın.') + '</div>';
      return;
    }
    liste.innerHTML = gosterilecek.map(function (k) {
      return '<div class="satir" data-id="' + kacar(k.id) + '">' +
        '<span class="yontem">' + kacar(k.method) + '</span>' +
        '<span class="yol">' + kacar(k.path) + '</span>' +
        '<span class="durum ' + durumSinifi(k.status) + '">' + k.status + '</span>' +
        '<span class="alt">' + zaman(k.started) + ' · ' + kacar(k.host) +
        ' · ' + kacar(k.duration) + ' · ' + boyut(k.size) + '</span></div>';
    }).join('');
    if (secili) isaretle(secili);
  }

  function isaretle(id) {
    Array.prototype.forEach.call(liste.querySelectorAll('.satir'), function (el) {
      el.classList.toggle('secili', el.dataset.id === id);
    });
  }

  function basliklarTablosu(h) {
    var anahtarlar = Object.keys(h || {}).sort();
    if (!anahtarlar.length) return '<div class="bos">başlık yok</div>';
    return '<table class="basliklar">' + anahtarlar.map(function (k) {
      return '<tr><td>' + kacar(k) + '</td><td>' +
        kacar((h[k] || []).join(', ')) + '</td></tr>';
    }).join('') + '</table>';
  }

  function govde(metin, kesildi) {
    if (!metin) return '<div class="bos">gövde yok</div>';
    // Gövde her zaman metin olarak gösteriliyor, asla HTML olarak
    // basılmıyor: kaydedilen içerik incelenen uygulamadan geliyor.
    return '<pre>' + kacar(metin) + '</pre>' +
      (kesildi ? '<div class="uyari">Gövde kesildi (bellek sınırı).</div>' : '');
  }

  function detayCiz(k) {
    detay.innerHTML =
      '<div class="baslik">' +
        '<b>' + kacar(k.method) + '</b> <span class="yol">' + kacar(k.path) +
        (k.query ? kacar('?' + k.query) : '') + '</span>' +
        '<span class="durum ' + durumSinifi(k.status) + '">' + k.status + '</span>' +
        '<span class="rozet">' + kacar(k.duration) + '</span>' +
        '<span class="rozet">' + boyut(k.responseSize) + '</span>' +
        '<button id="tekrar">Tekrar gönder</button>' +
      '</div>' +
      '<div class="bolum"><h3>İstek başlıkları</h3>' + basliklarTablosu(k.requestHeaders) + '</div>' +
      '<div class="bolum"><h3>İstek gövdesi</h3>' + govde(k.requestBody, k.requestTruncated) + '</div>' +
      '<div class="bolum"><h3>Yanıt başlıkları</h3>' + basliklarTablosu(k.responseHeaders) + '</div>' +
      '<div class="bolum"><h3>Yanıt gövdesi</h3>' + govde(k.responseBody, k.responseTruncated) + '</div>' +
      '<div class="bolum" id="tekrarSonuc" hidden></div>';

    document.getElementById('tekrar').addEventListener('click', function () {
      var dugme = this;
      dugme.disabled = true;
      dugme.textContent = 'Gönderiliyor…';
      fetch('/api/exchanges/' + encodeURIComponent(k.id) + '/replay', { method: 'POST' })
        .then(function (r) { return r.json().then(function (g) { return { ok: r.ok, g: g }; }); })
        .then(function (sonuc) {
          var kutu = document.getElementById('tekrarSonuc');
          kutu.hidden = false;
          if (!sonuc.ok) {
            kutu.innerHTML = '<h3>Tekrar sonucu</h3><div class="uyari">' +
              kacar(sonuc.g.error) + '</div>';
            return;
          }
          kutu.innerHTML = '<h3>Tekrar sonucu</h3>' +
            '<p><span class="durum ' + durumSinifi(sonuc.g.status) + '">' + sonuc.g.status +
            '</span> · ' + kacar(sonuc.g.duration) + ' · ' + boyut(sonuc.g.size) + '</p>' +
            basliklarTablosu(sonuc.g.headers) + govde(sonuc.g.body, false);
        })
        .catch(function (e) {
          var kutu = document.getElementById('tekrarSonuc');
          kutu.hidden = false;
          kutu.innerHTML = '<div class="uyari">' + kacar(e.message) + '</div>';
        })
        .finally(function () {
          dugme.disabled = false;
          dugme.textContent = 'Tekrar gönder';
        });
    });
  }

  liste.addEventListener('click', function (e) {
    var satir = e.target.closest('.satir');
    if (!satir) return;
    secili = satir.dataset.id;
    isaretle(secili);
    fetch('/api/exchanges/' + encodeURIComponent(secili))
      .then(function (r) { return r.json(); })
      .then(detayCiz);
  });

  ara.addEventListener('input', listeyiCiz);

  document.getElementById('temizle').addEventListener('click', function () {
    fetch('/api/exchanges', { method: 'DELETE' }).then(function () {
      kayitlar = [];
      secili = null;
      detay.innerHTML = '<div class="bos">Soldan bir istek seçin.</div>';
      listeyiCiz();
    });
  });

  function durumCiz(s) {
    kayitDugme.textContent = 'Kayıt: ' + (s.enabled ? 'açık' : 'kapalı');
    kayitDugme.classList.toggle('acik', s.enabled);
    kayitDugme.dataset.enabled = s.enabled ? '1' : '';
    document.getElementById('alan').textContent = s.domain || '';
  }

  kayitDugme.addEventListener('click', function () {
    fetch('/api/state', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: !kayitDugme.dataset.enabled })
    }).then(function (r) { return r.json(); }).then(durumCiz);
  });

  function yenile() {
    fetch('/api/exchanges').then(function (r) { return r.json(); })
      .then(function (d) { kayitlar = d; listeyiCiz(); });
    fetch('/api/state').then(function (r) { return r.json(); }).then(durumCiz);
  }

  var akis = new EventSource('/api/stream');
  akis.onmessage = function (e) {
    kayitlar.unshift(JSON.parse(e.data));
    if (kayitlar.length > 400) kayitlar.pop();
    listeyiCiz();
  };

  yenile();
})();
</script>
</body>
</html>
`
