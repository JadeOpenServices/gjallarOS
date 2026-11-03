# pkgs/noctalia/notification-app-name.patch: a notification's app name is
# looked up without the identity-token guess, so "GjallarOS" stays
# "GjallarOS" next to a "gjallarOS-ai" desktop entry instead of showing as
# "gjallarOS AI". Compiles the patched app_identity.cpp from the shipped
# sources with a small test; the package itself builds without tests.
{ pkgs, noctalia }:
let
  src = pkgs.srcOnly noctalia;
in
pkgs.runCommandCC "noctalia-notification-app-name" { } ''
  cat > test.cpp <<'CPP'
  #include "system/app_identity.h"
  #include "system/internal_app_metadata.h"
  #include "util/string_utils.h"

  #include <array>
  #include <cstdio>

  namespace internal_apps {
    std::optional<AppMetadata> metadataForAppId(std::string_view) { return std::nullopt; }
    void applyMetadataToDesktopEntry(DesktopEntry&) {}
  }

  static DesktopEntry entry(const char* id, const char* name) {
    DesktopEntry e;
    e.id = id;
    e.idLower = StringUtils::toLower(e.id);
    e.name = name;
    e.nameLower = StringUtils::toLower(e.name);
    return e;
  }

  static int failed = 0;
  static void check(bool ok, const char* what) {
    std::printf("%s: %s\n", ok ? "ok" : "FAIL", what);
    failed += ok ? 0 : 1;
  }

  int main() {
    const auto entries = std::array<DesktopEntry, 2>{
        entry("gjallarOS-ai", "gjallarOS AI"),
        entry("org.mozilla.firefox", "Firefox"),
    };
    check(app_identity::findDesktopEntry("GjallarOS", entries).has_value(),
          "running app ids still use the token guess");
    check(!app_identity::findDesktopEntryStrict("GjallarOS", entries).has_value(),
          "GjallarOS is not gjallarOS AI");
    const auto byName = app_identity::findDesktopEntryStrict("Firefox", entries);
    check(byName && byName->id == "org.mozilla.firefox", "name match");
    const auto byTail = app_identity::findDesktopEntryStrict("com.example.firefox", entries);
    check(byTail && byTail->id == "org.mozilla.firefox", "reverse-DNS tail match");
    const auto exact = app_identity::findDesktopEntryStrict("gjallarOS-ai", entries);
    check(exact && exact->id == "gjallarOS-ai", "id match");
    return failed;
  }
  CPP
  grep -q findDesktopEntryStrict ${src}/src/notification/notification_display_name.cpp
  $CXX -std=c++23 -Wall -Werror -I${src}/src test.cpp ${src}/src/system/app_identity.cpp -o test
  ./test
  touch $out
''
