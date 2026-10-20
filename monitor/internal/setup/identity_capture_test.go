package setup

// planIdentities are the gfx1151 catalogue's identities at 4d40a3a4f
// (TestPlanIdentitiesSurviveWordingChanges): plan IDs, whole saved plans,
// and the picture, coding and starter model-plan IDs. The plan identities
// changed once with the catalogue's content on 2026-11-11, when the OA build
// of ComfyUI lost its PyTorch link and its pin (owner: OA builds no ComfyUI);
// the model-plan IDs did not.
var planIdentities = map[string]string{
	"[comfyui]":                        "8c37a0baee0923a1003903edb8b1a4bda2dd11afc1161b31cf0b571d16e29e04",
	"[pytorch]":                        "d9a131626b7256756db9b519001e1ced297c9178932c729446bdd0d89f4d69fa",
	"[thenoise]":                       "6cab9ccbde81a89feeea9cb9b4945b0a2f5b7615fda9ae2bec5150e0ead10e6d",
	"[comfyui pytorch thenoise]":       "204e0da3ae76dff10522c6f42e055ef6b5bf0df9dd83cee4e5f9e8c5301a8164",
	"[comfyui thenoise]":               "611a7e1a4b792ded5fd572ac1f5803f3c41b17d5aa149553c2d83f6c16591a56",
	"saved [comfyui]":                  "7603906472d4b92f78a1b5c0cd880c2bfc7c6e904aae015bb769d57b95f7c808",
	"saved [pytorch]":                  "5836a529563f314b8adf51121f929ac1f5712c0a3224622dda02911f382c0f68",
	"saved [thenoise]":                 "c8a361f8d6fdda27fc75dd65c72de0f3dd3a044d58f2efe015420788d4571cf7",
	"saved [comfyui pytorch thenoise]": "6500b8e5060b53ffedb5a684b36e2b4a239664aec7ef23edc7f4a61f9ce6182f",
	"saved [comfyui thenoise]":         "ffb9f79c312148e70fd6b630ba5882ba007a2bfece345fdc70fe86bff4101e42",
	"FLUX.2 Klein 4B compact":          "fb55c30b3a2033b9d15f2c3b892110684189c0381cd33110532dc585736ef707",
	"FLUX.2 Klein 4B full precision":   "efce852a5085946e4eb4d4f61e520b81e5dbb3bef8c5d3d042f39bb89e572553",
	"Z-Image-Turbo":                    "fac481977fec914568d09fe8124c6774867b9fc9f95e9fbb4850c6a8131227a5",
	"coding":                           "66bd7252ddae9808c545e9458b8edb7f856f37c9db4f7f09c13613059f1df472",
	"lemonade starter":                 "11e0b867586694b78900ab8ecba79f824b8370e7dd096a3fa71e1bf107ed5aeb",
	"thenoise starter":                 "055c5c156bb3f1610b956b33357c78a0eac86945ecbb29778562bab8f4836a11",
}
