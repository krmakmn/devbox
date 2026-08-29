// devbox.yaml'daki Xdebug anahtarını çeviren küçük düzenleyici.
//
// Neden YAML kütüphanesi yok: eklenti tek bir satırı değiştiriyor.
// Dosyayı ayrıştırıp yeniden yazmak, kullanıcının yorumlarını, sırasını
// ve biçimini bozardı — bir düzenleyici eklentisinin yapabileceği en can
// sıkıcı şey. Bu yüzden metin üzerinde en küçük değişiklik yapılıyor.

/** Xdebug satırının bulunduğu ve yeni hâli. */
export interface XdebugEdit {
  /** content, dosyanın yeni içeriği. */
  content: string;
  /** enabled, değişiklikten sonraki durum. */
  enabled: boolean;
}

/** xdebugEnabled, yapılandırmada Xdebug'ın açık olup olmadığını söyler. */
export function xdebugEnabled(content: string): boolean {
  return /^[ \t]*xdebug:[ \t]*true[ \t]*$/m.test(content);
}

/**
 * toggleXdebug, php.xdebug anahtarını çevirir.
 *
 * Üç durum var: satır varsa değeri çevriliyor; php bloğu varsa satır o
 * bloğa ekleniyor; hiçbiri yoksa blok dosyanın sonuna yazılıyor. Girinti
 * dosyada kullanılan girintiden alınıyor, sabit iki boşluk varsayılmıyor.
 */
export function toggleXdebug(content: string): XdebugEdit {
  // \s yerine [ \t]: çok satırlı kipte \s satır sonunu da yutuyor ve
  // değiştirme dosyanın son satır sonunu siliyordu. Kullanıcının
  // deposunda gereksiz bir fark bırakmak, bir düzenleyici eklentisinin
  // yapabileceği en can sıkıcı şeylerden biri. Test bunu sabitliyor.
  const line = /^([ \t]*)xdebug:[ \t]*(true|false)[ \t]*$/m;
  const match = content.match(line);
  if (match) {
    const enabled = match[2] !== 'true';
    return {
      content: content.replace(line, `${match[1]}xdebug: ${enabled}`),
      enabled,
    };
  }

  const phpBlock = /^php:\s*$/m;
  if (phpBlock.test(content)) {
    const indent = detectIndent(content);
    return {
      content: content.replace(phpBlock, `php:\n${indent}xdebug: true`),
      enabled: true,
    };
  }

  const indent = detectIndent(content);
  const suffix = content.endsWith('\n') ? '' : '\n';
  return {
    content: `${content}${suffix}php:\n${indent}xdebug: true\n`,
    enabled: true,
  };
}

/**
 * detectIndent, dosyada kullanılan girintiyi bulur.
 *
 * devbox.yaml'ı Go tarafı yazdığında girinti dört boşluk oluyor; elle
 * yazan çoğu kişi iki kullanıyor. Var olanı korumak, dosyanın
 * eklentiden sonra da tutarlı görünmesini sağlıyor.
 */
export function detectIndent(content: string): string {
  const match = content.match(/^([ \t]+)\S/m);
  return match ? match[1] : '  ';
}
