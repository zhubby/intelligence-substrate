{{flutter_js}}
{{flutter_build_config}}

_flutter.loader.load({
  config: {
    // Missing glyphs must never cause an implicit request to a public font CDN.
    fontFallbackBaseUrl: new URL('assets/fonts/fallback/', document.baseURI).href,
  },
});
