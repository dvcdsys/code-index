// The cix wordmark. `cix` is short for CodeIndeX — the tag spells it out so
// the abbreviation reads instantly (brand rule: first mention always pairs
// the two forms).
//
// NOTE: og/og.html duplicates this art (it's a standalone prop page that
// can't import modules). If you change the art, update og.html and
// regenerate public/og-image.png — see site/README.md.
export const CIX_ART = ` ██████╗██╗██╗  ██╗
██╔════╝██║╚██╗██╔╝
██║     ██║ ╚███╔╝
██║     ██║ ██╔██╗
╚██████╗██║██╔╝ ██╗
 ╚═════╝╚═╝╚═╝  ╚═╝`;

export function CixAscii({ withTag, style }) {
  return (
    <pre className="cix-ascii" style={style} aria-hidden="true">
      {CIX_ART}{withTag && <span className="tag">CodeIndeX</span>}
    </pre>
  );
}
