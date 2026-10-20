package main

import (
	"context"
	"errors"
	"slices"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// choiceControllerFor is the controller of one offered picture model plan
// (setup.PictureModelChoices). An ID no offered plan has is refused.
func (p *servicePanel) choiceControllerFor(planID string) (setup.ModelPlan, setupModelController, error) {
	i := slices.IndexFunc(setup.PictureModelChoices(), func(plan setup.ModelPlan) bool { return plan.ID == planID })
	if i < 0 {
		return setup.ModelPlan{}, nil, errors.New("unknown picture model plan")
	}
	plan := setup.PictureModelChoices()[i]
	p.modelControllerMu.Lock()
	defer p.modelControllerMu.Unlock()
	if controller, ok := p.choiceControllers[plan.ID]; ok {
		return plan, controller, nil
	}
	controller, err := setup.DefaultChoiceController(plan)
	if err != nil {
		return setup.ModelPlan{}, nil, err
	}
	if p.choiceControllers == nil {
		p.choiceControllers = map[string]setupModelController{}
	}
	p.choiceControllers[plan.ID] = controller
	return plan, controller, nil
}

// choiceStatuses reads each offered plan's journal; a plan whose controller
// cannot be built is left out.
func (p *servicePanel) choiceStatuses(ctx context.Context) map[string]setup.ModelStatus {
	statuses := map[string]setup.ModelStatus{}
	for _, plan := range setup.PictureModelChoices() {
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
