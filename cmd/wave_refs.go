package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// refKind is either ConfigMap or Secret.
type refKind string

const (
	refConfigMap refKind = "ConfigMap"
	refSecret    refKind = "Secret"
)

// resourceRef is a single ConfigMap or Secret reference found inside a workload.
type resourceRef struct {
	kind     refKind
	name     string
	usedBy   string // e.g. "Deployment/web"
	waveIdx  int
	waveName string
}

// refIssue is a ref that is either missing entirely or applied in the wrong wave.
type refIssue struct {
	ref           resourceRef
	definedWave   int    // index of the wave that defines it, -1 if not in plan
	definedWaveNm string // wave name for display
	inCluster     bool   // only meaningful when definedWave == -1
}

func (i refIssue) label() string {
	switch {
	case i.definedWave < 0 && !i.inCluster:
		return "🔴 not found in plan or cluster"
	case i.definedWave < 0 && i.inCluster:
		return "🔵 not in plan but exists in cluster"
	case i.definedWave > i.ref.waveIdx:
		return fmt.Sprintf("🟡 defined in wave %d (%s) but used in wave %d (%s) — wrong order",
			i.definedWave+1, i.definedWaveNm, i.ref.waveIdx+1, i.ref.waveName)
	default:
		return ""
	}
}

// ── YAML helpers ──────────────────────────────────────────────────────────────

// asMap safely casts an interface{} to map[string]interface{}.
func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// asSlice safely casts an interface{} to []interface{}.
func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

// get walks a dotted path through nested maps.
func get(m map[string]interface{}, keys ...string) interface{} {
	var cur interface{} = m
	for _, k := range keys {
		cur = asMap(cur)[k]
		if cur == nil {
			return nil
		}
	}
	return cur
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

// ── manifest parsing ──────────────────────────────────────────────────────────

// splitYAMLDocs splits multi-document YAML on "---" boundaries.
func splitYAMLDocs(data []byte) [][]byte {
	var docs [][]byte
	for _, part := range bytes.Split(data, []byte("\n---")) {
		part = bytes.TrimSpace(part)
		if len(part) > 0 {
			docs = append(docs, part)
		}
	}
	return docs
}

// parseDoc decodes one YAML document into a generic map.
func parseDoc(data []byte) map[string]interface{} {
	var doc map[string]interface{}
	_ = yaml.Unmarshal(data, &doc)
	return doc
}

// kindName returns "kind" and "metadata.name" from a document.
func kindName(doc map[string]interface{}) (string, string) {
	return strings.ToLower(str(doc["kind"])), str(get(doc, "metadata", "name"))
}

// podSpecPath returns the path to the pod spec inside a workload document.
// Returns nil for unknown kinds.
func podSpecPath(kind string) []string {
	switch kind {
	case "deployment", "statefulset", "daemonset", "replicaset":
		return []string{"spec", "template", "spec"}
	case "job", "cronjob":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	case "pod":
		return []string{"spec"}
	}
	return nil
}

// refsFromPodSpec extracts all ConfigMap and Secret references from a pod spec map.
func refsFromPodSpec(spec map[string]interface{}, usedBy string, waveIdx int, waveName string) []resourceRef {
	var refs []resourceRef
	add := func(kind refKind, name string) {
		if name != "" {
			refs = append(refs, resourceRef{kind: kind, name: name, usedBy: usedBy, waveIdx: waveIdx, waveName: waveName})
		}
	}

	// collect from a container list (containers + initContainers)
	collectContainers := func(key string) {
		for _, ci := range asSlice(spec[key]) {
			c := asMap(ci)

			// envFrom
			for _, efi := range asSlice(c["envFrom"]) {
				ef := asMap(efi)
				add(refConfigMap, str(get(ef, "configMapRef", "name")))
				add(refSecret, str(get(ef, "secretRef", "name")))
			}

			// env[].valueFrom
			for _, ei := range asSlice(c["env"]) {
				e := asMap(ei)
				vf := asMap(e["valueFrom"])
				add(refConfigMap, str(get(vf, "configMapKeyRef", "name")))
				add(refSecret, str(get(vf, "secretKeyRef", "name")))
			}
		}

		// volumes
		for _, vi := range asSlice(spec["volumes"]) {
			v := asMap(vi)
			add(refConfigMap, str(get(v, "configMap", "name")))
			add(refSecret, str(get(v, "secret", "secretName")))
		}
	}

	collectContainers("containers")
	collectContainers("initContainers")
	return refs
}

// ── main check ────────────────────────────────────────────────────────────────

// CheckRefs scans every manifest in the plan, finds ConfigMap/Secret references
// inside workloads, and verifies each ref is either defined in the plan (in an
// earlier or equal wave) or exists in the cluster.
func CheckRefs(plan wavePlan) []refIssue {
	type defined struct {
		waveIdx  int
		waveName string
	}

	// Pass 1: collect every ConfigMap and Secret that the plan itself defines.
	planDefined := map[refKind]map[string]defined{}
	planDefined[refConfigMap] = map[string]defined{}
	planDefined[refSecret] = map[string]defined{}

	for wi, wave := range plan.Waves {
		for _, path := range wave.Manifests {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, raw := range splitYAMLDocs(data) {
				doc := parseDoc(raw)
				k, name := kindName(doc)
				switch k {
				case "configmap":
					planDefined[refConfigMap][name] = defined{wi, wave.Name}
				case "secret":
					planDefined[refSecret][name] = defined{wi, wave.Name}
				}
			}
		}
	}

	// Pass 2: collect all workload refs.
	var refs []resourceRef
	for wi, wave := range plan.Waves {
		for _, path := range wave.Manifests {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, raw := range splitYAMLDocs(data) {
				doc := parseDoc(raw)
				k, name := kindName(doc)
				specPath := podSpecPath(k)
				if specPath == nil {
					continue
				}
				spec := asMap(get(doc, specPath...))
				if spec == nil {
					continue
				}
				usedBy := str(doc["kind"]) + "/" + name
				refs = append(refs, refsFromPodSpec(spec, usedBy, wi, wave.Name)...)
			}
		}
	}

	// Pass 3: for each ref, determine if it's resolved.
	seen := map[string]bool{}
	var issues []refIssue

	for _, ref := range refs {
		key := string(ref.kind) + "/" + ref.name + "@" + ref.waveName + "/" + ref.usedBy
		if seen[key] {
			continue
		}
		seen[key] = true

		def, inPlan := planDefined[ref.kind][ref.name]
		if inPlan && def.waveIdx <= ref.waveIdx {
			// defined in same or earlier wave — all good
			continue
		}

		issue := refIssue{ref: ref, definedWave: -1}
		if inPlan {
			// defined but in a later wave
			issue.definedWave = def.waveIdx
			issue.definedWaveNm = def.waveName
		} else {
			// not in plan — check the cluster
			issue.inCluster = existsInCluster(ref.kind, ref.name, plan.Namespace)
		}

		if issue.label() != "" {
			issues = append(issues, issue)
		}
	}

	return issues
}

// existsInCluster returns true if the resource exists in the cluster.
func existsInCluster(kind refKind, name, namespace string) bool {
	var resource string
	switch kind {
	case refConfigMap:
		resource = "configmap"
	case refSecret:
		resource = "secret"
	}
	args := []string{"get", resource, name, "--ignore-not-found", "-o", "name"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}

// FormatRefIssues prints a human-readable summary of ref issues to stdout.
// Returns true if any blocking issues (🔴) were found.
func FormatRefIssues(issues []refIssue) bool {
	if len(issues) == 0 {
		fmt.Println("  ✓ All ConfigMap and Secret references are satisfied.")
		return false
	}

	hasBlocking := false
	for _, iss := range issues {
		label := iss.label()
		fmt.Printf("  %s  %s  →  %s/%s\n", label, iss.ref.usedBy, iss.ref.kind, iss.ref.name)
		if strings.HasPrefix(label, "🔴") {
			hasBlocking = true
		}
	}
	return hasBlocking
}

// RefIssueSummary returns a compact markdown block suitable for appending to
// the Claude prompt so the model is aware of the pre-checked findings.
func RefIssueSummary(issues []refIssue) string {
	if len(issues) == 0 {
		return "**Pre-check:** All ConfigMap and Secret references are satisfied (in plan or in cluster).\n\n"
	}
	var sb strings.Builder
	sb.WriteString("**Pre-check — reference issues detected (verify before applying):**\n\n")
	for _, iss := range issues {
		fmt.Fprintf(&sb, "- %s: `%s` references `%s/%s`\n", iss.label(), iss.ref.usedBy, iss.ref.kind, iss.ref.name)
	}
	sb.WriteString("\n")
	return sb.String()
}
