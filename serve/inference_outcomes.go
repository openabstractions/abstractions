package main

import iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"

func inferenceListRefusal(word string) iwire.ListOutcome {
	switch word {
	case "forbidden":
		return iwire.ListOutcomeForbidden
	case "unavailable":
		return iwire.ListOutcomeUnavailable
	}
	return 0
}

func inferenceEditRefusal(word string) iwire.EditOutcome {
	switch word {
	case "forbidden":
		return iwire.EditOutcomeForbidden
	case "unavailable":
		return iwire.EditOutcomeUnavailable
	}
	return 0
}

func inferenceAuditRefusal(word string) iwire.AuditOutcome {
	switch word {
	case "forbidden":
		return iwire.AuditOutcomeForbidden
	case "unavailable":
		return iwire.AuditOutcomeUnavailable
	}
	return 0
}
