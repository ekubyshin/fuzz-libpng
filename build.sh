#!/usr/bin/env bash
# Одна команда: zlib + libpng v1.6.48 (AFL++) + harness + seed в corpus/
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
DEPS="${ROOT}/third_party"
INSTALL="${ROOT}/install"
LIBPNG_TAG="${LIBPNG_TAG:-v1.6.48}"
# На zlib.net у старых версий часто 404 — берём релиз с GitHub, при неудаче fossils.
ZLIB_VER="${ZLIB_VER:-1.3.1}"

: "${AFL_CC:=afl-clang-fast}"
command -v "${AFL_CC}" >/dev/null 2>&1 || { echo "Нужен ${AFL_CC} (AFL++) в PATH" >&2; exit 1; }

export CC="${AFL_CC}" CXX="${AFL_CC}++"
export CFLAGS="${CFLAGS:--O2 -g}" CXXFLAGS="${CXXFLAGS:--O2 -g}"
mkdir -p "${DEPS}" "${INSTALL}"

echo "zlib ${ZLIB_VER}"
ZDIR="${DEPS}/zlib-${ZLIB_VER}"
if [[ ! -d "${ZDIR}" ]]; then
  z="zlib-${ZLIB_VER}"
  t="${DEPS}/${z}.tar.gz"
  urls=(
    "https://github.com/madler/zlib/releases/download/v${ZLIB_VER}/${z}.tar.gz"
    "https://zlib.net/fossils/${z}.tar.gz"
    "https://zlib.net/${z}.tar.gz"
  )
  ok=0
  for u in "${urls[@]}"; do
    echo "  curl: $u"
    rm -f "${t}"
    if curl -fsSL "$u" -o "${t}" && tar xzf "${t}" -C "${DEPS}"; then ok=1; rm -f "${t}"; break; fi
    rm -f "${t}"
  done
  [[ "${ok}" -eq 1 ]] || { echo "error: не удалось скачать ${z}" >&2; exit 1; }
fi
# Нельзя «make -j clean install» — clean и install идут параллельно и ломают сборку.
( cd "${ZDIR}" && ./configure --prefix="${INSTALL}" --static && make clean && make -j4 && make install )

export CPPFLAGS="-I${INSTALL}/include" LDFLAGS="-L${INSTALL}/lib" PKG_CONFIG_PATH="${INSTALL}/lib/pkgconfig"

echo "libpng ${LIBPNG_TAG}"
LP="${DEPS}/libpng"
[[ -d "${LP}/.git" ]] || git clone --depth 1 --branch "${LIBPNG_TAG}" https://github.com/pnggroup/libpng.git "${LP}"
git -C "${LP}" checkout "${LIBPNG_TAG}" 2>/dev/null || true
( cd "${LP}" && ./configure --prefix="${INSTALL}" --disable-shared --enable-static && make clean && make -j4 && make install )

echo "harness"
CFG="${INSTALL}/bin/libpng16-config"
[[ -x "${CFG}" ]] && LIBS="$("${CFG}" --static --cflags --libs)" || LIBS="${CPPFLAGS} -lpng16 -lz"
${CC} ${CFLAGS} -o "${ROOT}/png_read_harness" "${ROOT}/harness/png_read_harness.c" ${LDFLAGS} ${LIBS} -lm

python3 "${ROOT}/scripts/gen_corpus.py"
echo "Готово: ${ROOT}/png_read_harness"
