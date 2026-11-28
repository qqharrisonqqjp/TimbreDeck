// Package trainingtools exposes the same training scripts used by the local CLI.
package trainingtools

import "embed"

//go:embed prepare_gpt_dataset.py train_gpt_model.py
var Scripts embed.FS
