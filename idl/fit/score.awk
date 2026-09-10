BEGIN {
	FS = "\t"
	W["naming"] = 1; W["namespace"] = 1; W["errors"] = 2
	W["construction"] = 1; W["absence"] = 1; W["opaque"] = 1
	MAX = 14
	split("go python cpp javascript rust", L, " ")
	split("naming namespace errors construction absence opaque", T, " ")
}

$1 == "tool" {
	if (!($2 in toolseen)) { toolseen[$2] = 1 }
	if ($4 == "0")  { toolfail[$2] = toolfail[$2] $3 " "; }
	if ($4 == "-1") { toolgone[$2] = toolgone[$2] $3 " "; }
	tooltext[$2, $3] = $5
	toolstate[$2, $3] = $4
	tools[$2] = tools[$2] $3 " "
	next
}

$1 == "term" { pt[$2, $3] = $4; why[$2, $3] = $5; next }

END {
	printf "TOOL — the language's own community. A refusal here is a hard zero.\n\n"
	for (i = 1; i <= 5; i++) {
		l = L[i]
		n = split(tools[l], tl, " ")
		for (j = 1; j <= n; j++) {
			s = toolstate[l, tl[j]]
			v = (s == "1" ? "ok" : (s == "0" ? "FAIL" : "UNPROVEN"))
			printf "  %-11s %-12s %-9s %s\n", l, tl[j], v, tooltext[l, tl[j]]
		}
	}

	printf "\nRUBRIC — ours, from research/native-shape. Weaker evidence, and it says so.\n"
	printf "\n%-12s", "term"
	for (i = 1; i <= 5; i++) printf "%11s", L[i]
	printf "%6s\n", "wt"
	for (k = 1; k <= 6; k++) {
		t = T[k]
		printf "%-12s", t
		for (i = 1; i <= 5; i++) printf "%11d", pt[L[i], t]
		printf "%6d\n", W[t]
	}

	printf "\n%-12s", "points"
	for (i = 1; i <= 5; i++) {
		l = L[i]
		s = 0
		for (k = 1; k <= 6; k++) s += W[T[k]] * pt[l, T[k]]
		raw[l] = s
		gate[l] = (toolfail[l] != "")
		final[l] = gate[l] ? 0 : s
		printf "%11d", final[l]
	}
	printf "%6d\n", MAX
	printf "%-12s", "of 14"
	for (i = 1; i <= 5; i++) printf "%11s", sprintf("%.3f%s", final[L[i]] / MAX, toolgone[L[i]] != "" ? "?" : "")
	printf "\n"
	# The language with no tool scores highest, and it must not read as the best
	# one. A gate that goes green because a toolchain was absent is the defect
	# CLAUDE.md names by name, and a number is the easiest place for it to hide.
	for (i = 1; i <= 5; i++) if (toolgone[L[i]] != "")
		printf "\n  ? %s has no tool to refuse it. Its number is our judgement alone and\n    is not comparable with the four above it — a high score with no tool\n    behind it is an absence, not a pass.\n", L[i]

	printf "\nwhy each number is what it is\n"
	for (i = 1; i <= 5; i++) {
		l = L[i]
		printf "\n  %s", l
		if (gate[l]) printf " — HARD ZERO: %srefused the file. Rubric would have been %d of %d.", toolfail[l], raw[l], MAX
		if (toolgone[l] != "") {
			n = split(toolgone[l], gl, " ")
			for (j = 1; j <= n; j++) printf " — UNPROVEN: %s", tooltext[l, gl[j]]
		}
		printf "\n"
		for (k = 1; k <= 6; k++) {
			t = T[k]
			if (pt[l, t] < 2) printf "    %-13s %d  %s\n", t, pt[l, t], why[l, t]
		}
	}

	printf "\nseries\n"
	for (i = 1; i <= 5; i++) printf "fit_points_%s\t%d\n", L[i], final[L[i]]
}
