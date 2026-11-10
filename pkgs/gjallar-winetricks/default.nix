# Used by apps/wine: its setup run and gjallar-wine, whose fix table (Plane
# 277) is checked against this script's pins at build time.
{ winetricks }:

# The setup run's own winetricks (Q74). In unattended mode winetricks keeps
# going after a SHA256 mismatch when it has no dialog to ask, and accepts
# files with no checksum. Both stop the step here instead. --force (used by
# gjallar-wine repair to install a verb again) also skips both checksum
# checks, for a cached file and a fresh download; those stop the step too,
# so force only means "install again" (loophole 45). The Wayback
# Machine retry stays, since the hash still has to match. aka.ms always
# points at the newest VC runtime; the versioned URL it redirects to is
# named by the hash winetricks pins. A winetricks update that changes
# these lines fails the build until the pins are checked again.
#
# dotnet40/dotnet45 (and so dotnet48) set OnlyUseLatestCLR for 32-bit
# programs under a key that misses Microsoft\, so a later wineboot -u stops
# at a .NET dialog in its 32-bit pass and never ends (Plane 277). Same
# change as winetricks PR #2533, carried until it is merged (book 753).
#
# w_download_to checks the cache folder name with grep '\/-'; GNU grep 3.8
# and later warn "stray \ before /" on every download. '/-' matches the
# same (book 753).
#
# vcrun2022 asks cabextract for a name its pinned redist does not use, so
# Wine's own msvcp140 stayed in every prefix. Upstream PR #2495, carried
# until a release has it (book 753).
winetricks.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [ ./vcrun2022-msvcp140.patch ];
  postPatch = old.postPatch + ''
    substituteInPlace src/winetricks \
      --replace-fail "grep -q -e '\/-' -e '^-'" "grep -q -e '/-' -e '^-'" \
      --replace-fail 'HKLM\\Software\\Wow6432Node\\.NETFramework' \
        'HKLM\\Software\\Wow6432Node\\Microsoft\\.NETFramework' \
      --replace-fail 'w_askpermission "SHA256 mismatch!' \
        'w_die "SHA256 mismatch for ''${_W_url}"; w_askpermission "SHA256 mismatch!' \
      --replace-fail 'w_warn "sha256sum mismatch! However --force was used, so trying anyway. Caveat emptor."' \
        'w_die "SHA256 mismatch for ''${_W_url}"' \
      --replace-fail 'w_warn "Checksum for ''${_W_cache}/''${_W_file} did not match, but --force was used, so ignoring and trying anyway."' \
        'w_die "Checksum for ''${_W_cache}/''${_W_file} did not match"' \
      --replace-fail '    w_get_shatype "''${_W_sum}"' \
        '    w_get_shatype "''${_W_sum}"; test -n "''${_W_sum}" || w_die "No checksum for ''${_W_url}"' \
      --replace-fail \
        'https://aka.ms/vs/17/release/vc_redist.x86.exe 0c09f2611660441084ce0df425c51c11e147e6447963c3690f97e0b25c55ed64' \
        'https://download.visualstudio.microsoft.com/download/pr/990c720a-46ce-4dc7-87b9-c2eb615d34b9/0C09F2611660441084CE0DF425C51C11E147E6447963C3690F97E0B25C55ED64/VC_redist.x86.exe 0c09f2611660441084ce0df425c51c11e147e6447963c3690f97e0b25c55ed64 vc_redist.x86.exe' \
      --replace-fail \
        'https://aka.ms/vs/17/release/vc_redist.x64.exe cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b' \
        'https://download.visualstudio.microsoft.com/download/pr/990c720a-46ce-4dc7-87b9-c2eb615d34b9/CC0FF0EB1DC3F5188AE6300FAEF32BF5BEEBA4BDD6E8E445A9184072096B713B/VC_redist.x64.exe cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b vc_redist.x64.exe'
  '';
})
