#!/bin/sh

_cppenv_have() { command -v "$1" >/dev/null 2>&1; }

_cppenv_q() {
	printf "'"
	printf '%s' "$1" | sed "s/'/'\\\\''/g"
	printf "'"
}

_cppenv_u() {
	if _cppenv_have cygpath; then cygpath -u "$1"
	else printf '%s' "$1" | sed -e 's|\\|/|g' -e 's|^\([A-Za-z]\):|/\L\1|'; fi
}

_cppenv_w() {
	if _cppenv_have cygpath; then cygpath -w "$1"
	else printf '%s' "$1" | sed -e 's|^/\([A-Za-z]\)/|\U\1:/|' -e 's|/|\\|g'; fi
}

_cppenv_newest() {
	_n=
	_seen=0
	for _c in "$@"; do
		[ -e "$_c" ] || continue
		_seen=$((_seen + 1))
		_n=$_c
	done
	[ "$_seen" -gt 0 ] || return 1
	if [ "$_seen" -gt 1 ]; then
		_n=$(for _c in "$@"; do [ -e "$_c" ] && printf '%s\n' "$_c"; done | sort -V | tail -n1)
	fi
	printf '%s' "$_n"
}

_cppenv_find_vs() {
	_vs=
	_vs_how=
	_pf=${ProgramFiles:-${PROGRAMFILES:-${ProgramW6432:-}}}
	[ -n "$_pf" ] || { _vs_looked="no ProgramFiles in the environment, so no Visual Studio search"; return 1; }
	_pf=$(_cppenv_u "$_pf")
	_pf86="$_pf (x86)"
	[ -d "$_pf86" ] || _pf86=$_pf

	_vsw="$_pf86/Microsoft Visual Studio/Installer/vswhere.exe"
	_vs_looked="vswhere at $_vsw"
	if [ -x "$_vsw" ]; then
		_vs=$("$_vsw" -latest -products '*' \
			-requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 \
			-property installationPath 2>/dev/null | tr -d '\r' | head -n1)
		if [ -n "$_vs" ]; then
			_vs=$(_cppenv_u "$_vs")
			_vs_how="vswhere -latest -requires VC.Tools.x86.x64"
		fi
	fi

	if [ -z "$_vs" ]; then
		_vs_looked="$_vs_looked; vcvars64.bat under $_pf and $_pf86"
		_vs=$(_cppenv_newest \
			"$_pf"/"Microsoft Visual Studio"/*/*/VC/Auxiliary/Build/vcvars64.bat \
			"$_pf86"/"Microsoft Visual Studio"/*/*/VC/Auxiliary/Build/vcvars64.bat) || return 1
		_vs=${_vs%/VC/Auxiliary/Build/vcvars64.bat}
		_vs_how="glob under ProgramFiles, vswhere absent"
	fi

	_vcvars="$_vs/VC/Auxiliary/Build/vcvars64.bat"
	[ -f "$_vcvars" ] || return 1
	_cl=$(_cppenv_newest "$_vs"/VC/Tools/MSVC/*/bin/HostX64/x64/cl.exe) ||
		_cl=$(_cppenv_newest "$_vs"/VC/Tools/MSVC/*/bin/Host*/x64/cl.exe) || return 1
	_toolset=${_cl#*/VC/Tools/MSVC/}
	_toolset=${_toolset%%/*}
	_cmake="$_vs/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe"
	_ninja="$_vs/Common7/IDE/CommonExtensions/Microsoft/CMake/Ninja/ninja.exe"
	return 0
}

_cppenv_msvc_exports() {
	_ps=$(command -v powershell.exe 2>/dev/null) || _ps=$(command -v pwsh 2>/dev/null) || {
		_fail="no powershell on PATH to run vcvars64.bat through"
		return 1
	}
	_tmp=$(mktemp -d 2>/dev/null) || { _fail="mktemp -d failed"; return 1; }
	_bat="$_tmp/cppenv.bat"
	{
		printf '@echo off\r\n'
		printf 'call "%s" >nul 2>&1\r\n' "$(_cppenv_w "$_vcvars")"
		printf 'set\r\n'
	} >"$_bat"

	_extra=
	for _t in "$_cmake" "$_ninja"; do
		[ -x "$_t" ] && _extra="$_extra${_t%/*}:"
	done

	_out=$("$_ps" -NoProfile -NonInteractive -Command "& '$(_cppenv_w "$_bat")'" 2>/dev/null |
		awk -v oldpath="$PATH" -v extra="$_extra" -v tag="msvc $_toolset" '
		function q(v,   n, a, i, s) {
			n = split(v, a, "\047")
			s = a[1]
			for (i = 2; i <= n; i++) s = s "\047\\\047\047" a[i]
			return "\047" s "\047"
		}
		function unixify(e) {
			gsub(/\\/, "/", e)
			if (e ~ /^[A-Za-z]:\//) e = "/" tolower(substr(e, 1, 1)) substr(e, 3)
			sub(/\/$/, "", e)
			return e
		}
		BEGIN {
			n = split(oldpath ":" extra, a, ":")
			for (i = 1; i <= n; i++) if (a[i] != "") have[a[i]] = 1
			n = split(extra, a, ":")
			for (i = 1; i <= n; i++) if (a[i] != "") add[++k] = a[i]
		}
		{
			sub(/\r$/, "")
			p = index($0, "=")
			if (p == 0) next
			key = toupper(substr($0, 1, p - 1))
			val = substr($0, p + 1)
			if (key == "VCTOOLSINSTALLDIR") ok = 1
			if (key == "PATH" || key == "INCLUDE" || key == "LIB" || key == "LIBPATH") v[key] = val
		}
		END {
			if (!ok) { print "cppenv: vcvars64.bat ran but set no VCToolsInstallDir" > "/dev/stderr"; exit 1 }
			n = split(v["PATH"], a, ";")
			for (i = 1; i <= n; i++) {
				e = unixify(a[i])
				if (e == "" || e in have) continue
				have[e] = 1
				add[++k] = e
			}
			s = ""
			for (i = 1; i <= k; i++) s = s (i == 1 ? "" : ":") add[i]
			if (s != "") print "export PATH=" q(s) ":\"$PATH\""
			split("INCLUDE LIB LIBPATH", w, " ")
			for (i = 1; i <= 3; i++) if (v[w[i]] != "") print "export " w[i] "=" q(v[w[i]])
			print "export CC=" q("cl")
			print "export CXX=" q("cl")
			print "export ABSTRACTION_CPPENV=" q(tag)
		}') || { rm -rf "$_tmp"; _fail="vcvars64.bat produced no usable environment"; return 1; }

	rm -rf "$_tmp"
	[ -n "$_out" ] || { _fail="vcvars64.bat produced no usable environment"; return 1; }
	printf '%s\n' "$_out"
}

_cppenv_version() {
	case "$1" in
		*cl.exe | cl) "$1" 2>&1 </dev/null | sed -n '/ersion/{s/\r$//;p;q;}' ;;
		*) "$1" --version 2>&1 </dev/null | sed -n '1{s/\r$//;s/^cmake version //;p;q;}' ;;
	esac
}

_cppenv_report() {
	printf 'toolchain  %s\n' "$_kind"
	printf 'compiler   %s\n' "$_cxx"
	printf 'version    %s\n' "$(_cppenv_version "$_cxx")"
	[ -z "${_toolset:-}" ] || printf 'toolset    %s\n' "$_toolset"
	if [ -n "${_cmake:-}" ] && [ -x "$_cmake" ]; then
		printf 'cmake      %s (%s)\n' "$_cmake" "$(_cppenv_version "$_cmake")"
	elif _cppenv_have cmake; then
		printf 'cmake      %s (%s)\n' "$(command -v cmake)" "$(_cppenv_version cmake)"
	else
		printf 'cmake      UNPROVEN: none found\n'
	fi
	printf 'found by   %s\n' "$_how"
}

_cppenv_main() {
	_kind=
	_cmake=
	_toolset=
	_vs_looked=
	case "${1:-report}" in
		--report | report) _mode=report ;;
		--export) _mode=export ;;
		*) echo "usage: cppenv.sh [--report|--export]" >&2; return 2 ;;
	esac

	if _cppenv_have cl; then
		_kind=msvc
		_cxx=$(command -v cl)
		_how="already on PATH"
		[ "$_mode" = report ] && { _cppenv_report; return 0; }
		printf 'export ABSTRACTION_CPPENV=%s\n' "$(_cppenv_q msvc)"
		return 0
	fi

	if _cppenv_find_vs; then
		_kind=msvc
		_cxx=$_cl
		_how=$_vs_how
		[ "$_mode" = report ] && { _cppenv_report; return 0; }
		_cppenv_msvc_exports && return 0
		echo "cppenv: UNPROVEN: found $_vcvars but could not use it: $_fail" >&2
		return 1
	fi

	for _c in ${CXX:-} g++ clang++ c++; do
		_cppenv_have "$_c" || continue
		_kind=$_c
		_cxx=$(command -v "$_c")
		_how="on PATH"
		break
	done

	if [ -z "$_kind" ]; then
		echo "cppenv: UNPROVEN: no C++ toolchain on this machine" >&2
		echo "cppenv: looked for cl, g++, clang++, c++ on PATH; $_vs_looked" >&2
		return 1
	fi

	[ "$_mode" = report ] && { _cppenv_report; return 0; }
	printf 'export CXX=%s\n' "$(_cppenv_q "$_cxx")"
	printf 'export ABSTRACTION_CPPENV=%s\n' "$(_cppenv_q "$_kind")"
}

case "$0" in
	*cppenv.sh)
		_cppenv_main "$@"
		exit $?
		;;
	*)
		_cppenv_out=$(_cppenv_main --export) && eval "$_cppenv_out"
		;;
esac
