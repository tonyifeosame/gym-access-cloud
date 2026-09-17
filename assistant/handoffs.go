package assistant

import (
	"access-terminal-cloud-api/models"
)

// Hand-offs: where the console takes over.
//
// FIVE ROUTES, AND ONLY THESE. A hand-off is built here from an identifier
// the tool already validated (registry.go's Identifier rule, then Segment),
// never from a route the model wrote. Anything the console can be sent to
// from the assistant is one of these constructors; phase2_test.go
// (TestHandoffsAreTheFiveKnownRoutes) holds the constructors to these
// shapes, and assistant_phase2_test.go holds every route the tools emit.
//
//	/people/:id            the person's page
//	/people/:id?enrol=1    the person's page with the enrolment dialog open
//	/terminals/:serial     the terminal's page
//	/terminals?pending=1   the fleet page, scrolled to terminals waiting to be set up
//	/sites/:id             the site's page

const (
	HandoffPerson           = "person"
	HandoffEnrolment        = "enrolment"
	HandoffTerminal         = "terminal"
	HandoffPendingTerminals = "pending_terminals"
	HandoffSite             = "site"
)

func handoffPerson(externalID, label string) *models.AssistantHandoff {
	return &models.AssistantHandoff{
		Kind:  HandoffPerson,
		Route: "/people/" + Segment(externalID),
		Label: "Open " + label,
	}
}

func handoffEnrolment(externalID, label string) *models.AssistantHandoff {
	return &models.AssistantHandoff{
		Kind:  HandoffEnrolment,
		Route: "/people/" + Segment(externalID) + "?enrol=1",
		Label: "Open the enrolment screen for " + label,
	}
}

func handoffTerminal(serial, label string) *models.AssistantHandoff {
	return &models.AssistantHandoff{
		Kind:  HandoffTerminal,
		Route: "/terminals/" + Segment(serial),
		Label: "Open " + label,
	}
}

func handoffPendingTerminals() *models.AssistantHandoff {
	return &models.AssistantHandoff{
		Kind:  HandoffPendingTerminals,
		Route: "/terminals?pending=1",
		Label: "Open terminals waiting to be set up",
	}
}

func handoffSite(siteID, label string) *models.AssistantHandoff {
	return &models.AssistantHandoff{
		Kind:  HandoffSite,
		Route: "/sites/" + Segment(siteID),
		Label: "Open " + label,
	}
}
