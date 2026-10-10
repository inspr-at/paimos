// SPDX-License-Identifier: AGPL-3.0-only
package cli

import "strconv"

type benefitFlags struct{ PillEN, PillDE, BenefitEN, BenefitDE, Hide, NoReleaseNeeded string }

func (b *benefitFlags) flags(fs *flagSet) {
	fs.string(&b.PillEN, "pill-en", 0, "English benefit pill (2–4 words)")
	fs.string(&b.PillDE, "pill-de", 0, "German benefit pill (2–4 words)")
	fs.string(&b.BenefitEN, "benefit-en", 0, "English user benefit (one or two sentences)")
	fs.string(&b.BenefitDE, "benefit-de", 0, "German user benefit (one or two neutral sentences)")
	fs.string(&b.Hide, "hide-from-release-notes", 0, "true or false; does not waive benefits")
	fs.string(&b.NoReleaseNeeded, "no-release-needed", 0, "true or false; content work needs no software release and is omitted from release notes")
}
func (b benefitFlags) changed() bool {
	return b.PillEN+b.PillDE+b.BenefitEN+b.BenefitDE+b.Hide+b.NoReleaseNeeded != ""
}
func (b benefitFlags) apply(fields map[string]any) (bool, error) {
	for key, value := range map[string]string{"pill_en": b.PillEN, "pill_de": b.PillDE, "benefit_en": b.BenefitEN, "benefit_de": b.BenefitDE} {
		if value != "" {
			fields[key] = value
		}
	}
	if b.Hide != "" {
		if b.Hide != "true" && b.Hide != "false" {
			return false, usagef("--hide-from-release-notes must be true or false")
		}
		value, _ := strconv.ParseBool(b.Hide)
		fields["hide_from_release_notes"] = value
	}
	if b.NoReleaseNeeded != "" {
		if b.NoReleaseNeeded != "true" && b.NoReleaseNeeded != "false" {
			return false, usagef("--no-release-needed must be true or false")
		}
		value, _ := strconv.ParseBool(b.NoReleaseNeeded)
		fields["no_release_needed"] = value
	}
	return b.changed(), nil
}
