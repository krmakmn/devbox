package api

// panelHTML, DevBox denetim paneli.
//
// # Neden Tauri değil (şimdilik)
//
// Yol haritası masaüstü uygulaması için Tauri diyordu. Tauri bir Rust
// derleme zinciri ve platform başına bir web görünümü kütüphanesi
// gerektiriyor; Windows'ta derlenip çalıştırılmadan doğruluğu
// gösterilemeyecek bir katman. Bu depoda kural, yazılan her şeyin
// çalıştığının gösterilmesi.
//
// Bu yüzden arayüz çekirdek sürecin sunduğu yerel bir sayfa: aynı API'yi
// kullanıyor, tek dosya, derleme adımı yok, çevrimdışı çalışıyor ve gerçek
// bir tarayıcıda sınanabiliyor. Tauri (ya da Windows'ta WebView2) kabuğu
// sonradan bu adrese bakan ince bir sarmalayıcı olarak eklenebilir —
// mimari onu dışlamıyor, sadece kanıtlanabilir olanı önce yapıyor.
//
// # Neden dış kaynak yok
//
// Geliştirme ortamı internetsiz de açılmalı. Tek dosya, gömülü simge,
// CDN yok.
const panelHTML = `<!doctype html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DevBox</title>
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
           border-bottom:1px solid var(--kenar); background:var(--ikincil); }
  header h1 { font-size:15px; margin:0; font-weight:600; }
  header .sag { margin-left:auto; display:flex; gap:10px; align-items:center;
                color:var(--soluk); font-size:12px; }
  main { display:grid; grid-template-columns:minmax(320px,380px) 1fr;
         height:calc(100vh - 49px); }
  #sol { overflow-y:auto; border-right:1px solid var(--kenar); }
  #sag { display:flex; flex-direction:column; min-width:0; }
  .proje { padding:12px 14px; border-bottom:1px solid var(--kenar); cursor:pointer; }
  .proje:hover { background:var(--ikincil); }
  .proje.secili { background:var(--ikincil); box-shadow:inset 3px 0 0 var(--vurgu); }
  .proje .ust { display:flex; align-items:center; gap:8px; }
  .proje .ad { font-weight:600; }
  .proje .alan, .proje .yol { color:var(--soluk); font-size:12px;
                              overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .nokta { width:8px; height:8px; border-radius:50%; background:var(--soluk); flex:0 0 auto; }
  .nokta.acik { background:var(--iyi); }
  .nokta.hata { background:var(--kotu); }
  .nokta.eksik { background:var(--uyari); }
  .dugmeler { margin-left:auto; display:flex; gap:6px; }
  button { font:inherit; padding:3px 10px; border:1px solid var(--kenar);
           border-radius:6px; background:var(--zemin); color:var(--metin); cursor:pointer; }
  button:hover:not(:disabled) { border-color:var(--vurgu); }
  button:disabled { opacity:.5; cursor:default; }
  .baslik { padding:10px 16px; border-bottom:1px solid var(--kenar);
            display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
  .baslik h2 { margin:0; font-size:14px; }
  .baslik a { color:var(--vurgu); }
  #suzgec { font:inherit; padding:3px 8px; width:200px; border-radius:6px;
            border:1px solid var(--kenar); background:var(--zemin); color:var(--metin); }
  #suzgec:focus { outline:none; border-color:var(--vurgu); }
  #gunluk { flex:1; overflow:auto; margin:0; padding:10px 16px;
            font:12px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace;
            white-space:pre-wrap; word-break:break-word; }
  #gunluk .vurgulu { background:rgba(37,99,235,.22); border-radius:2px; }
  .bos { padding:24px 16px; color:var(--soluk); }
  .rozet { font-size:11px; padding:1px 6px; border:1px solid var(--kenar);
           border-radius:999px; color:var(--soluk); }
  .hatamesaji { margin:10px 16px; padding:8px 10px; border-radius:6px;
                border:1px solid var(--kotu); color:var(--kotu); font-size:13px; }
  .saglik { display:flex; gap:14px; flex-wrap:wrap; padding:8px 16px;
            border-bottom:1px solid var(--kenar); color:var(--soluk); font-size:12px; }
  .saglik b { color:var(--metin); font-weight:600; }
  .olcum { color:var(--soluk); font-size:12px; font-variant-numeric:tabular-nums; }
  .sekmeler { display:flex; gap:2px; padding:0 16px; border-bottom:1px solid var(--kenar);
              background:var(--ikincil); }
  .sekmeler button { background:none; border:none; border-bottom:2px solid transparent;
                     padding:8px 12px; color:var(--soluk); cursor:pointer; font:inherit; }
  .sekmeler button.secili { color:var(--metin); border-bottom-color:var(--vurgu); font-weight:600; }
  #ayarlar { flex:1; overflow:auto; padding:16px; display:none; }
  #ayarlar.acik { display:block; }
  #gunluk.gizli { display:none; }
  .alan { margin-bottom:16px; max-width:520px; }
  .alan label { display:block; font-weight:600; margin-bottom:4px; }
  .alan .ipucu { color:var(--soluk); font-size:12px; margin-bottom:6px; }
  .alan select, .alan input[type=text], .alan textarea {
    width:100%; padding:6px 8px; border:1px solid var(--kenar); border-radius:4px;
    background:var(--zemin); color:var(--metin); font:inherit; }
  .alan textarea { min-height:70px; font-family:ui-monospace,Consolas,monospace; font-size:12px; }
  .ayarUyari { padding:8px 10px; border-radius:4px; margin-bottom:14px; font-size:13px;
               background:rgba(217,119,6,.14); border:1px solid var(--uyari); }
  .ayarSonuc { padding:8px 10px; border-radius:4px; margin-bottom:14px; font-size:13px; }
  .ayarSonuc.iyi { background:rgba(22,163,74,.14); border:1px solid var(--iyi); }
  .ayarSonuc.kotu { background:rgba(220,38,38,.14); border:1px solid var(--kotu); }
  #ayarlar pre.hamyapilandirma { background:var(--ikincil); border:1px solid var(--kenar);
    border-radius:4px; padding:10px; overflow:auto; font-size:12px; max-width:520px; }
</style>
</head>
<body>
<header>
  <h1>DevBox</h1>
  <span class="rozet" id="surum">—</span>
  <span class="sag">
    <span id="durum">bağlanılıyor…</span>
    <button id="yenile">Yenile</button>
  </span>
</header>
<div class="saglik" id="saglik"></div>
<main>
  <div id="sol"><div class="bos">Yükleniyor…</div></div>
  <div id="sag">
    <div class="baslik" id="detayBaslik"><h2>Bir proje seçin</h2></div>
    <div class="sekmeler" id="sekmeler">
      <button data-sekme="gunluk" class="secili">Günlük</button>
      <button data-sekme="ayarlar">Ayarlar</button>
    </div>
    <pre id="gunluk"></pre>
    <div id="ayarlar"></div>
  </div>
</main>
<script>
(function () {
  var sol = document.getElementById('sol');
  var gunluk = document.getElementById('gunluk');
  var detayBaslik = document.getElementById('detayBaslik');
  var durum = document.getElementById('durum');
  var saglik = document.getElementById('saglik');
  var secili = null;
  var akis = null;
  // İşlemci oranı iki yoklama arasındaki farktan çıkıyor; API birikmiş
  // süreyi veriyor, durumu burada tutuyoruz.
  var oncekiOlcum = {};
  var satirlar = [];
  var suzgecMetni = '';

  function boyut(bayt) {
    if (!bayt) return '';
    var birimler = ['B', 'KB', 'MB', 'GB'];
    var i = 0, d = bayt;
    while (d >= 1024 && i < birimler.length - 1) { d /= 1024; i++; }
    return (d >= 10 || i === 0 ? Math.round(d) : d.toFixed(1)) + ' ' + birimler[i];
  }

  // islemciOrani, birikmiş işlemci süresinden iki yoklama arasındaki
  // kullanımı hesaplar. İlk ölçümde karşılaştıracak bir şey olmadığı için
  // boş dönüyor — sıfır yazmak yanıltıcı olurdu.
  function islemciOrani(ad, p) {
    var simdi = Date.now() / 1000;
    var onceki = oncekiOlcum[ad];
    oncekiOlcum[ad] = { cpu: p.cpuSeconds || 0, an: simdi };
    if (!onceki || !p.running) return '';
    var gecen = simdi - onceki.an;
    if (gecen <= 0) return '';
    var oran = ((p.cpuSeconds || 0) - onceki.cpu) / gecen * 100;
    if (oran < 0) return '';
    return (oran < 10 ? oran.toFixed(1) : Math.round(oran)) + '% işlemci';
  }

  function kacar(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c];
    });
  }

  function istek(yol, secenekler) {
    return fetch(yol, secenekler || {}).then(function (r) {
      if (!r.ok) {
        return r.json().catch(function () { return { error: r.statusText }; })
          .then(function (g) { throw new Error(g.error || r.statusText); });
      }
      return r.status === 204 ? null : r.json();
    });
  }

  function hataGoster(mesaj) {
    var eski = document.querySelector('.hatamesaji');
    if (eski) eski.remove();
    if (!mesaj) return;
    var el = document.createElement('div');
    el.className = 'hatamesaji';
    el.textContent = mesaj;
    document.getElementById('sag').insertBefore(el, gunluk);
  }

  function projeleriCiz(liste) {
    if (!liste.length) {
      sol.innerHTML = '<div class="bos">Kayıtlı proje yok.<br><br>' +
        'Bir proje dizininde <b>devbox project add</b> çalıştırın.</div>';
      return;
    }
    sol.innerHTML = liste.map(function (p) {
      var sinif = p.running ? 'acik' : (p.missing ? 'eksik' : (p.error ? 'hata' : ''));
      var dugme = p.running
        ? '<button data-eylem="stop" data-ad="' + kacar(p.name) + '">Durdur</button>'
        : '<button data-eylem="start" data-ad="' + kacar(p.name) + '"' +
          (p.missing || p.error ? ' disabled' : '') + '>Başlat</button>';
      var olcum = [];
      if (p.running) {
        var cpu = islemciOrani(p.name, p);
        if (p.rss) olcum.push(boyut(p.rss));
        if (cpu) olcum.push(cpu);
      } else {
        islemciOrani(p.name, p);
      }
      return '<div class="proje" data-ad="' + kacar(p.name) + '">' +
        '<div class="ust"><span class="nokta ' + sinif + '"></span>' +
        '<span class="ad">' + kacar(p.name) + '</span>' +
        '<span class="dugmeler">' + dugme + '</span></div>' +
        '<div class="alan">' + (p.domain ? 'https://' + kacar(p.domain) : '') +
        (p.restarts ? ' · ' + p.restarts + ' yeniden başlatma' : '') + '</div>' +
        '<div class="yol">' + kacar(p.dir) + '</div>' +
        (olcum.length ? '<div class="olcum">' + kacar(olcum.join(' · ')) +
          ' <span title="Ölçüm devbox up sürecinin kendisi; php-cgi işçileri ' +
          've veritabanı ayrı süreçler">(ana süreç)</span></div>' : '') +
        (p.error ? '<div class="alan" style="color:var(--kotu)">' + kacar(p.error) + '</div>' : '') +
        '</div>';
    }).join('');
    if (secili) isaretle(secili);
    var s = liste.filter(function (p) { return p.running; }).length;
    saglik.innerHTML =
      '<span><b>' + liste.length + '</b> proje kayıtlı</span>' +
      '<span><b>' + s + '</b> çalışıyor</span>' +
      '<span><b>' + liste.filter(function (p) { return p.missing; }).length +
      '</b> dizini eksik</span>';
  }

  function isaretle(ad) {
    Array.prototype.forEach.call(sol.querySelectorAll('.proje'), function (el) {
      el.classList.toggle('secili', el.dataset.ad === ad);
    });
  }

  function yenile() {
    return istek('/v1/projects').then(projeleriCiz).then(function () {
      durum.textContent = 'bağlı';
    }).catch(function (e) {
      durum.textContent = 'bağlantı yok';
      hataGoster(e.message);
    });
  }

  function gunlugüCiz() {
    var q = suzgecMetni.toLowerCase();
    var gosterilecek = q
      ? satirlar.filter(function (l) { return l.toLowerCase().indexOf(q) >= 0; })
      : satirlar;
    if (!gosterilecek.length) {
      gunluk.innerHTML = '<span class="bos">' +
        (q ? 'Süzgece uyan satır yok.' : 'Henüz günlük yok.') + '</span>';
      return;
    }
    gunluk.innerHTML = gosterilecek.map(function (l) {
      if (!q) return kacar(l);
      // Eşleşen parçayı vurgula: uzun satırda nerede geçtiği görünsün.
      var i = l.toLowerCase().indexOf(q);
      return kacar(l.slice(0, i)) + '<span class="vurgulu">' +
        kacar(l.slice(i, i + q.length)) + '</span>' + kacar(l.slice(i + q.length));
    }).join('\n');
    gunluk.scrollTop = gunluk.scrollHeight;
  }

  function projeSec(ad, p) {
    secili = ad;
    isaretle(ad);
    hataGoster(null);
    satirlar = [];

    detayBaslik.innerHTML = '<h2>' + kacar(ad) + '</h2>' +
      (p && p.domain ? '<a href="https://' + kacar(p.domain) + '" target="_blank" rel="noopener">https://' +
        kacar(p.domain) + '</a>' : '') +
      (p && p.server ? '<span class="rozet">' + kacar(p.server) + '</span>' : '') +
      (p && p.pid ? '<span class="rozet">pid ' + p.pid + '</span>' : '') +
      '<input id="suzgec" type="search" placeholder="Günlükte ara" autocomplete="off">';

    var suzgec = document.getElementById('suzgec');
    suzgec.value = suzgecMetni;
    suzgec.addEventListener('input', function () {
      suzgecMetni = suzgec.value;
      gunlugüCiz();
    });

    if (acikSekme === 'ayarlar') ayarlariYukle();

    if (akis) { akis.close(); akis = null; }
    gunluk.textContent = '';
    if (!p || !p.serviceName) return;

    // Hiç başlatılmamış projenin denetçide servisi yok; akışı açmak
    // 404 üretirdi. Durum zaten belli, boşuna sormuyoruz.
    if (!p.state) {
      gunluk.innerHTML = '<span class="bos">Proje henüz başlatılmadı.</span>';
      return;
    }

    akis = new EventSource('/v1/services/' + encodeURIComponent(p.serviceName) + '/logs/stream');
    akis.onmessage = function (e) {
      satirlar.push(e.data);
      // Bellek sınırı: uzun süren bir projede günlük sınırsız büyümesin.
      if (satirlar.length > 5000) satirlar = satirlar.slice(-5000);
      gunlugüCiz();
    };
    akis.onerror = function () {
      // Proje henüz hiç başlatılmadıysa servis yok; bu bir hata değil.
      if (!satirlar.length) {
        gunluk.innerHTML = '<span class="bos">Proje henüz başlatılmadı.</span>';
      }
    };
  }

  // --- Ayarlar sekmesi ---------------------------------------------------

  var ayarlar = document.getElementById('ayarlar');
  var sekmeler = document.getElementById('sekmeler');
  var acikSekme = 'gunluk';

  sekmeler.addEventListener('click', function (e) {
    var d = e.target.closest('button[data-sekme]');
    if (!d) return;
    sekmeSec(d.dataset.sekme);
  });

  function sekmeSec(ad) {
    acikSekme = ad;
    Array.prototype.forEach.call(sekmeler.children, function (b) {
      b.classList.toggle('secili', b.dataset.sekme === ad);
    });
    gunluk.classList.toggle('gizli', ad !== 'gunluk');
    ayarlar.classList.toggle('acik', ad === 'ayarlar');
    if (ad === 'ayarlar') ayarlariYukle();
  }

  function ayarlariYukle() {
    if (!secili) {
      ayarlar.innerHTML = '<div class="bos">Önce bir proje seçin.</div>';
      return;
    }
    ayarlar.innerHTML = '<div class="bos">Yükleniyor…</div>';
    fetch('/v1/projects/' + encodeURIComponent(secili) + '/config')
      .then(function (r) {
        return r.json().then(function (j) {
          if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
          return j;
        });
      })
      .then(ayarlariCiz)
      .catch(function (err) {
        ayarlar.innerHTML = '<div class="ayarSonuc kotu"></div>';
        ayarlar.firstChild.textContent = 'Ayarlar okunamadı: ' + err.message;
      });
  }

  function ayarlariCiz(veri) {
    var c = veri.config || {};
    var php = c.php || {};
    var secenekler = veri.options || {};
    var surumler = secenekler.phpVersions || [];

    // Yapılandırmada yazan sürüm kurulu değilse listeye ekleniyor.
    // Aksi hâlde açılır kutu onu göstermez ve kaydetmek, kullanıcının
    // hiç dokunmadığı bir alanı sessizce değiştirirdi.
    var mevcut = php.version || '';
    if (mevcut && surumler.indexOf(mevcut) < 0) surumler = [mevcut].concat(surumler);

    var h = '';
    h += '<div class="alan"><label for="aySunucu">Sunucu</label>' +
      '<div class="ipucu">İsteği kim karşılasın: DevBox\'ın kendi sunucusu, Apache, ' +
      'Nginx ya da başka bir adrese ileten vekil.</div>' +
      '<select id="aySunucu">' +
      (secenekler.servers || []).map(function (v) {
        return '<option' + (v === c.server ? ' selected' : '') + '>' + kacar(v) + '</option>';
      }).join('') + '</select></div>';

    h += '<div class="alan"><label for="aySurum">PHP sürümü</label>' +
      '<div class="ipucu">Yalnız kurulu sürümler listeleniyor. Kurmak için: ' +
      '<b>devbox runtime install php@8.3</b></div>' +
      '<select id="aySurum">' +
      '<option value=""' + (mevcut ? '' : ' selected') + '>(en yenisi)</option>' +
      surumler.map(function (v) {
        return '<option' + (v === mevcut ? ' selected' : '') + '>' + kacar(v) + '</option>';
      }).join('') + '</select></div>';

    h += '<div class="alan"><label for="ayIsci">PHP işçi sayısı</label>' +
      '<div class="ipucu">Boş bırakılırsa işlemci çekirdeği kadar.</div>' +
      '<input id="ayIsci" type="text" inputmode="numeric" value="' +
      (php.workers ? kacar(String(php.workers)) : '') + '"></div>';

    h += '<div class="alan"><label for="ayUzanti">PHP uzantıları</label>' +
      '<div class="ipucu">Virgülle ayırın: redis, gd, intl</div>' +
      '<input id="ayUzanti" type="text" value="' +
      kacar((php.extensions || []).join(', ')) + '"></div>';

    h += '<div class="alan"><label for="ayTakma">Takma alan adları</label>' +
      '<div class="ipucu">Virgülle ayırın. Her biri için sertifika üretilir.</div>' +
      '<input id="ayTakma" type="text" value="' +
      kacar((c.aliases || []).join(', ')) + '"></div>';

    h += '<div class="alan"><button id="ayKaydet">Kaydet</button></div>';
    h += '<div id="aySonuc"></div>';
    h += '<div class="alan"><label>devbox.yaml</label>' +
      '<div class="ipucu">Dosyanın diskteki hâli. Yorumlarınız korunur.</div>' +
      '<pre class="hamyapilandirma" id="ayHam"></pre></div>';

    ayarlar.innerHTML = h;
    document.getElementById('ayHam').textContent = veri.raw || '';
    document.getElementById('ayKaydet').addEventListener('click', ayarlariKaydet);

    // Yüklenen değerler saklanıyor; kaydederken yalnız FARK gönderilecek.
    yuklenen = formDegerleri();
  }

  var yuklenen = null;

  // formDegerleri, formdaki alanları Update'in beklediği biçime çevirir.
  function formDegerleri() {
    var isci = document.getElementById('ayIsci').value.trim();
    return {
      'server': document.getElementById('aySunucu').value,
      'php.version': document.getElementById('aySurum').value || null,
      'php.workers': isci ? parseInt(isci, 10) : null,
      'php.extensions': listeyeCevir(document.getElementById('ayUzanti').value),
      'aliases': listeyeCevir(document.getElementById('ayTakma').value)
    };
  }

  // listeyeCevir, virgülle ayrılmış metni diziye çevirir. Boş girdi
  // null döner: alanın silinmesi demek.
  function listeyeCevir(metin) {
    var parcalar = metin.split(',').map(function (s) { return s.trim(); })
      .filter(function (s) { return s.length > 0; });
    return parcalar.length ? parcalar : null;
  }

  // ayni, iki değerin (metin, sayı, dizi ya da null) eşitliğini söyler.
  function ayni(a, b) {
    if (Array.isArray(a) || Array.isArray(b)) {
      if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
      for (var i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
      return true;
    }
    return a === b;
  }

  function ayarlariKaydet() {
    var sonuc = document.getElementById('aySonuc');
    var isci = document.getElementById('ayIsci').value.trim();
    if (isci && !/^[0-9]+$/.test(isci)) {
      sonuc.className = 'ayarSonuc kotu';
      sonuc.textContent = 'İşçi sayısı tam sayı olmalı.';
      return;
    }

    // Yalnız DEĞİŞEN alanlar gönderiliyor.
    //
    // Bu bir güvenlik payı, incelik değil. Formu olduğu gibi göndermek
    // gerçek bir veri kaybına yol açmıştı: yükleme sırasında alanlar boş
    // kaldığı için kaydetme, kullanıcının hiç dokunmadığı "php.version"ı
    // ve o satırdaki yorumunu sildi. Fark göndermek, yükleme bir gün
    // yine eksik kalırsa bile dokunulmayan alanı korur.
    var suanki = formDegerleri();
    var degisiklikler = {};
    var sayi = 0;
    for (var k in suanki) {
      if (!yuklenen || !ayni(suanki[k], yuklenen[k])) {
        degisiklikler[k] = suanki[k];
        sayi++;
      }
    }
    if (!sayi) {
      sonuc.className = 'ayarSonuc';
      sonuc.textContent = 'Değişiklik yok.';
      return;
    }

    sonuc.className = 'ayarSonuc';
    sonuc.textContent = 'Kaydediliyor…';

    fetch('/v1/projects/' + encodeURIComponent(secili) + '/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ changes: degisiklikler })
    })
      .then(function (r) {
        return r.json().then(function (j) {
          if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
          return j;
        });
      })
      .then(function (j) {
        document.getElementById('ayHam').textContent = j.raw || '';
        yuklenen = formDegerleri();
        sonuc.className = 'ayarSonuc iyi';
        sonuc.textContent = j.restartNeeded
          ? 'Kaydedildi. Etkili olması için projeyi durdurup yeniden başlatın.'
          : 'Kaydedildi.';
        yenile();
      })
      .catch(function (err) {
        sonuc.className = 'ayarSonuc kotu';
        sonuc.textContent = 'Kaydedilemedi: ' + err.message;
      });
  }

  sol.addEventListener('click', function (e) {
    var dugme = e.target.closest('button[data-eylem]');
    if (dugme) {
      e.stopPropagation();
      var eylem = dugme.dataset.eylem, ad = dugme.dataset.ad;
      dugme.disabled = true;
      dugme.textContent = eylem === 'start' ? 'Başlatılıyor…' : 'Durduruluyor…';
      hataGoster(null);
      istek('/v1/projects/' + encodeURIComponent(ad) + '/' + eylem, { method: 'POST' })
        .then(function () { return yenile(); })
        .then(function () {
          if (secili === ad) {
            istek('/v1/projects/' + encodeURIComponent(ad)).then(function (p) {
              projeSec(ad, p);
            });
          }
        })
        .catch(function (err) { hataGoster(err.message); return yenile(); });
      return;
    }
    var satir = e.target.closest('.proje');
    if (!satir) return;
    istek('/v1/projects/' + encodeURIComponent(satir.dataset.ad))
      .then(function (p) { projeSec(satir.dataset.ad, p); })
      .catch(function (err) { hataGoster(err.message); });
  });

  document.getElementById('yenile').addEventListener('click', function () { yenile(); });

  istek('/v1/status').then(function (s) {
    document.getElementById('surum').textContent = 'API v' + s.apiVersion;
  }).catch(function () {});

  yenile();
  // Durum değişiklikleri (çöken bir proje, biten yeniden başlatma) kendi
  // kendine görünsün; SSE yalnız günlük için.
  setInterval(yenile, 5000);
})();
</script>
</body>
</html>
`
