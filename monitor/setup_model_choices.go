package main

import (
	"context"
	"errors"
	"slices"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func engineChoices(engine setup.AppID, fixed string) []setup.ModelPlan {
	return setup.PictureChoicesForEngine(engine, fixed)
}

// isEngineChoice reports an offered plan of engine with id.
func isEngineChoice(engine setup.AppID, id string) bool {
	return slices.ContainsFunc(engineChoices(engine, ""), func(plan setup.ModelPlan) bool { return id != "" && plan.ID == id })
}

// choiceControllerFor is the controller of one offered picture model plan
// (engineChoices). An ID no offered plan has is refused.
func (p *servicePanel) choiceControllerFor(planID string) (setup.ModelPlan, setupModelController, error) {
	plan, engine, ok := setup.FindPictureChoice(planID)
	if !ok {
		return setup.ModelPlan{}, nil, errors.New("unknown picture model plan")
	}
	p.modelControllerMu.Lock()
	defer p.modelControllerMu.Unlock()
	if controller, ok := p.choiceControllers[plan.ID]; ok {
		return plan, controller, nil
	}
	controller, err := setup.OpenPictureChoiceController(engine, plan, p.setupJournalForPanel)
	if err != nil {
		return setup.ModelPlan{}, nil, err
	}
	if p.choiceControllers == nil {
		p.choiceControllers = map[string]setupModelController{}
	}
	p.choiceControllers[plan.ID] = controller
	return plan, controller, nil
}

// choiceStatuses reads each of engine's offered plans' journals; a plan
// whose controller cannot be built is left out.
func (p *servicePanel) choiceStatuses(ctx context.Context, engine setup.AppID, fixed string) map[string]setup.ModelStatus {
	statuses := map[string]setup.ModelStatus{}
	for _, plan := range engineChoices(engine, fixed) {
		_, controller, err := p.choiceControllerFor(plan.ID)
		if err != nil {
			continue
		}
		if status, err := controller.Status(ctx); err == nil {
			statuses[plan.ID] = status
		}
	}
	return statuses
}
