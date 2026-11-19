{{/*
Helpers for templates/package-policy.yaml. They emit CEL source.

Two Kubernetes CEL constraints shape everything here:
- Map and list literals must be homogeneous, so every value in a rendered map
  literal is wrapped in dyn(...).
- CEL has no user-defined functions, so image normalisation is a template that
  expands to an expression over its argument, and is repeated wherever needed.
*/}}

{{/*
JSON (and so CEL) list of RE2 patterns anchored to match the whole string.
mustRegexMatch (Go's RE2, the engine Kubernetes CEL uses) fails the render on a
bad pattern; without that, a bad pattern would make the policy error on every
request and, with failurePolicy Fail, block every write. Both the raw and the
wrapped pattern are compiled: an unbalanced pattern such as `a)|(b` compiles
only inside the wrapper, where it would escape the anchors, and one such as
`\Qx` compiles only on its own, swallowing the wrapper's closing `)$`.
*/}}
{{- define "nodewright.packagePolicy.patterns" -}}
{{- $out := list -}}
{{- range . -}}
{{- $p := printf "^(?:%s)$" . -}}
{{- $_ := mustRegexMatch . "" -}}
{{- $_ = mustRegexMatch $p "" -}}
{{- $out = append $out $p -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/*
Whether the image reference expression in "." names a registry host. Following
the reference grammar the container runtime uses, a first path segment is a
host when it contains "." or ":", is "localhost", or is not all lower case;
otherwise the reference is a Docker Hub path.
*/}}
{{- define "nodewright.packagePolicy.hasHost" -}}
({{ . }}.contains('/') && ({{ . }}.split('/')[0].contains('.') || {{ . }}.split('/')[0].contains(':') || {{ . }}.split('/')[0] == 'localhost' || {{ . }}.split('/')[0].lowerAscii() != {{ . }}.split('/')[0]))
{{- end -}}

{{/*
Registry host of the image reference expression in ".": lower-cased, without a
default :443 port, and with Docker Hub's aliases folded into docker.io, so a
deny entry for a registry also catches the other spellings that pull from it.
*/}}
{{- define "nodewright.packagePolicy.host" -}}
{{- $h := printf "%s.split('/')[0].lowerAscii()" . -}}
{{- $h443 := printf "(%s.endsWith(':443') ? %s.substring(0, size(%s) - 4) : %s)" $h $h $h $h -}}
({{ include "nodewright.packagePolicy.hasHost" . }} ? ({{ $h443 }} in ['index.docker.io', 'registry-1.docker.io', 'registry.hub.docker.com'] ? 'docker.io' : {{ $h443 }}) : 'docker.io')
{{- end -}}

{{- define "nodewright.packagePolicy.path" -}}
({{ include "nodewright.packagePolicy.hasHost" . }} ? {{ . }}.substring(size({{ . }}.split('/')[0]) + 1) : {{ . }})
{{- end -}}

{{/*
Normalised host/path of the image reference expression in ".": single-segment
Docker Hub names gain library/, as the container runtime resolves them.
*/}}
{{- define "nodewright.packagePolicy.ref" -}}
{{- $host := include "nodewright.packagePolicy.host" . -}}
{{- $path := include "nodewright.packagePolicy.path" . -}}
({{ $host }} + '/' + (({{ $host }} == 'docker.io' && !{{ $path }}.contains('/')) ? 'library/' + {{ $path }} : {{ $path }}))
{{- end -}}

{{/*
The CEL string expression in "." without a digest and without a tag in its last
path segment, so a ":port" in the host is kept.
*/}}
{{- define "nodewright.packagePolicy.repo" -}}
{{- $s := printf "%s.split('@')[0]" . -}}
({{ $s }}.lastIndexOf(':') > {{ $s }}.lastIndexOf('/') ? {{ $s }}.substring(0, {{ $s }}.lastIndexOf(':')) : {{ $s }})
{{- end -}}

{{/*
CEL predicate: the value expression in "." is a zero value. A Go client writes
fields the stored object leaves out as their zero value (struct fields as {},
for example), so a zero value and an absent field compare equal below.
*/}}
{{- define "nodewright.packagePolicy.zero" -}}
({{ . }} == false || {{ . }} == 0 || {{ . }} == '' || {{ . }} == {} || {{ . }} == [])
{{- end -}}

{{/*
CEL predicate: the two value expressions in the list "." are equal. When the
CEL predicate in the third element holds, two maps are compared one level deep
with zero values treated as absent; otherwise they must be identical.
*/}}
{{- define "nodewright.packagePolicy.sameValue" -}}
{{- $a := index . 0 -}}
{{- $b := index . 1 -}}
{{- $deep := index . 2 -}}
({{ $a }} == {{ $b }} || ({{ $deep }} && type({{ $a }}) == map && type({{ $b }}) == map && {{ $a }}.all(g, g in {{ $b }} ? {{ $a }}[g] == {{ $b }}[g] : {{ include "nodewright.packagePolicy.zero" (printf "%s[g]" $a) }}) && {{ $b }}.all(g, g in {{ $a }} || {{ include "nodewright.packagePolicy.zero" (printf "%s[g]" $b) }})))
{{- end -}}

{{/*
CEL predicate: maps .a and .b are equal field by field, treating a field as
absent when it is missing, holds a zero value, or matches .ignore, a CEL
predicate over the field name f in which @v stands for the field's value.
Comparing this way keeps a typed client's full Update, which sends every field,
from reading as a change to fields it did not touch.

.deep is a CEL predicate over f naming the struct fields whose own fields are
compared the same way, because a typed client drops the empty lists and maps a
YAML author wrote inside them. Every other field, a user-data map such as
configMap above all, is compared exactly: there an added key with an empty
value is a change, and a field added to the API later fails closed.
*/}}
{{- define "nodewright.packagePolicy.sameMap" -}}
{{- $absentA := printf "(%s || %s)" (replace "@v" (printf "%s[f]" .a) .ignore) (include "nodewright.packagePolicy.zero" (printf "%s[f]" .a)) -}}
{{- $absentB := printf "(%s || %s)" (replace "@v" (printf "%s[f]" .b) .ignore) (include "nodewright.packagePolicy.zero" (printf "%s[f]" .b)) -}}
({{ .a }}.all(f, {{ $absentA }} ? (!(f in {{ .b }}) || {{ $absentB }}) : (f in {{ .b }} && !{{ $absentB }} && {{ include "nodewright.packagePolicy.sameValue" (list (printf "%s[f]" .a) (printf "%s[f]" .b) .deep) }})) && {{ .b }}.all(f, f in {{ .a }} || {{ $absentB }}))
{{- end -}}

{{/*
CEL list with one view per package in the map variable named by ".", carrying
the normalised fields every check reads. inlineRef marks an image with a tag or
digest of its own, and nameMismatch a package whose name field differs from its
key; the CRD's webhook refuses both, and the policy refuses them too, so it
does not depend on the webhook running.
*/}}
{{- define "nodewright.packagePolicy.view" -}}
{{- $p := printf "variables.%s[k]" . -}}
{{- $image := printf "%s.image" $p -}}
{{- $agent := printf "%s.agentImageOverride" $p -}}
{{- $hasAgent := printf "(has(%s) && %s != '')" $agent $agent -}}
{{- $agentRef := include "nodewright.packagePolicy.ref" $agent -}}
variables.{{ . }}.map(k, {"name": dyn(k), "image": dyn({{ $image }}), "inlineRef": dyn({{ $image }}.contains('@') || {{ $image }}.lastIndexOf(':') > {{ $image }}.lastIndexOf('/')), "nameMismatch": dyn(has({{ $p }}.name) && {{ $p }}.name != k), "host": dyn({{ include "nodewright.packagePolicy.host" $image }}), "ref": dyn({{ include "nodewright.packagePolicy.ref" $image }}), "version": dyn(has({{ $p }}.version) ? {{ $p }}.version : ''), "sha": dyn(has({{ $p }}.containerSHA) ? {{ $p }}.containerSHA : ''), "agent": dyn({{ $hasAgent }} ? {{ $agentRef }} : ''), "agentHost": dyn({{ $hasAgent }} ? {{ include "nodewright.packagePolicy.host" $agent }} : ''), "agentRepo": dyn({{ $hasAgent }} ? {{ include "nodewright.packagePolicy.repo" $agentRef }} : '')})
{{- end -}}

{{/*
CEL map literal for one change item built from view "v". Takes a dict with op
and startsUninstall (a CEL boolean expression).
*/}}
{{- define "nodewright.packagePolicy.item" -}}
{"op": dyn('{{ .op }}'), "name": v.name, "image": v.image, "inlineRef": v.inlineRef, "nameMismatch": v.nameMismatch, "host": v.host, "ref": v.ref, "version": v.version, "sha": v.sha, "agent": v.agent, "agentHost": v.agentHost, "agentRepo": v.agentRepo, "startsUninstall": dyn({{ .startsUninstall }})}
{{- end -}}

{{/*
Map-literal fields shared by deny entries and allow rules.
*/}}
{{- define "nodewright.packagePolicy.matchFields" -}}
"registries": dyn({{ include "nodewright.packagePolicy.patterns" (.registries | default list) }}), "images": dyn({{ include "nodewright.packagePolicy.patterns" (.images | default list) }}), "versions": dyn({{ include "nodewright.packagePolicy.patterns" (.versions | default list) }}), "shas": dyn({{ toJson (.shas | default list) }})
{{- end -}}

{{- define "nodewright.packagePolicy.denyLiteral" -}}
{{- $out := list -}}
{{- range . -}}
{{- $out = append $out (printf "{%s}" (include "nodewright.packagePolicy.matchFields" .)) -}}
{{- end -}}
[{{ join ", " $out }}]
{{- end -}}

{{/*
CEL list literal of the allow rules. Subjects are flattened to the usernames
and groups the apiserver reports in request.userInfo.
*/}}
{{- define "nodewright.packagePolicy.allowLiteral" -}}
{{- $out := list -}}
{{- range $name, $r := . -}}
{{- $users := list -}}
{{- $groups := list -}}
{{- range $s := ($r.subjects | default list) -}}
{{- if eq $s.kind "ServiceAccount" -}}
{{- $users = append $users (printf "system:serviceaccount:%s:%s" $s.namespace $s.name) -}}
{{- else if eq $s.kind "User" -}}
{{- $users = append $users $s.name -}}
{{- else -}}
{{- $groups = append $groups $s.name -}}
{{- end -}}
{{- end -}}
{{- $shas := $r.shas | default list -}}
{{- $requireDigest := or ($r.requireDigest | default false) (gt (len $shas) 0) -}}
{{- $fields := list (printf "\"name\": dyn(%s)" (toJson $name)) -}}
{{- $fields = append $fields (printf "\"anyone\": dyn(%s)" (toJson ($r.anyone | default false))) -}}
{{- $fields = append $fields (printf "\"rbac\": dyn(%s)" (toJson ($r.rbac | default false))) -}}
{{- $fields = append $fields (printf "\"users\": dyn(%s)" (toJson $users)) -}}
{{- $fields = append $fields (printf "\"groups\": dyn(%s)" (toJson $groups)) -}}
{{- $fields = append $fields (include "nodewright.packagePolicy.matchFields" $r) -}}
{{- $fields = append $fields (printf "\"operations\": dyn(%s)" (toJson ($r.operations | default (list "Add" "Update" "Remove")))) -}}
{{- $fields = append $fields (printf "\"agentImages\": dyn(%s)" (include "nodewright.packagePolicy.patterns" ($r.agentImages | default list))) -}}
{{- $fields = append $fields (printf "\"requireDigest\": dyn(%s)" (toJson $requireDigest)) -}}
{{- $out = append $out (printf "{%s}" (join ", " $fields)) -}}
{{- end -}}
[{{ join ", " $out }}]
{{- end -}}

{{/*
Sorted names of the allow rules that set rbac: true.
*/}}
{{- define "nodewright.packagePolicy.rbacNames" -}}
{{- $out := list -}}
{{- range $name, $r := . -}}
{{- if $r.rbac -}}
{{- $out = append $out $name -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/*
CEL predicate: deny entry or allow rule "." matches change item i. Every field
the entry sets must match; an unset field (empty list) matches anything.
*/}}
{{- define "nodewright.packagePolicy.match" -}}
((size({{ . }}.registries) == 0 || {{ . }}.registries.exists(p, i.host.matches(p))) && (size({{ . }}.images) == 0 || {{ . }}.images.exists(p, i.ref.matches(p))) && (size({{ . }}.versions) == 0 || {{ . }}.versions.exists(p, i.version.matches(p))) && (size({{ . }}.shas) == 0 || i.sha in {{ . }}.shas))
{{- end -}}

{{/*
CEL predicate: allow rule "." covers change item i. Removals and updates that
only start an uninstall retire a package, and a rule guards retiring every
package its registries and images cover, whatever versions or digests it
currently allows, so narrowing versions never hands older packages to every CR
writer. Adds and other updates must match every field the rule sets.
*/}}
{{- define "nodewright.packagePolicy.covers" -}}
((i.op == 'Remove' || i.startsUninstall) ? ((size({{ . }}.registries) == 0 || {{ . }}.registries.exists(p, i.host.matches(p))) && (size({{ . }}.images) == 0 || {{ . }}.images.exists(p, i.ref.matches(p)))) : {{ include "nodewright.packagePolicy.match" . }})
{{- end -}}

{{/*
CEL predicate: allow rule r covers change item i, allows its operation (Remove,
for an update that only starts an uninstall, since that retires the package),
and, for an Add or Update, allows its agent override and meets its digest requirement.
Removals and updates that only start an uninstall change neither, so those
checks do not apply to them.
*/}}
{{- define "nodewright.packagePolicy.ruleAllows" -}}
({{ include "nodewright.packagePolicy.covers" "r" }} && (i.startsUninstall ? 'Remove' in r.operations : i.op in r.operations) && (i.op == 'Remove' || i.startsUninstall || ((i.agent == '' || r.agentImages.exists(p, i.agent.matches(p))) && (!r.requireDigest || i.sha != ''))))
{{- end -}}

{{/*
CEL predicate: change item i is refused whoever asks: its image carries an
inline tag or digest, or a deny entry matches it or its agent override. A deny
entry applies to the override when it sets only registries and/or images,
matched against the override's host and its repository without tag or digest.
Removals and updates that only start an uninstall are never refused here, so a
package deployed before the policy can always be retired.
*/}}
{{- define "nodewright.packagePolicy.refused" -}}
(i.op != 'Remove' && !i.startsUninstall && (i.inlineRef || i.nameMismatch || variables.deny.exists(d, {{ include "nodewright.packagePolicy.match" "d" }}) || (i.agentHost != '' && variables.deny.exists(d, size(d.versions) == 0 && size(d.shas) == 0 && (size(d.registries) == 0 || d.registries.exists(p, i.agentHost.matches(p))) && (size(d.images) == 0 || d.images.exists(p, i.agentRepo.matches(p)))))))
{{- end -}}

{{/*
CEL predicate: change item i is allowed without consulting RBAC: there are no
allow rules, or it retires a package no allow rule covers, or a covering rule
grants it to anyone or names the requester.
*/}}
{{- define "nodewright.packagePolicy.allowedWithoutRBAC" -}}
(!{{ include "nodewright.packagePolicy.refused" . }} && (size(variables.allow) == 0 || ((i.op == 'Remove' || i.startsUninstall) && !variables.allow.exists(r, {{ include "nodewright.packagePolicy.covers" "r" }})) || variables.allow.exists(r, {{ include "nodewright.packagePolicy.ruleAllows" . }} && (r.anyone || request.userInfo.username in r.users || r.groups.exists(g, g in variables.groups)))))
{{- end -}}

{{/*
CEL predicate: an rbac: true allow rule allows change item i, if RBAC grants it.
*/}}
{{- define "nodewright.packagePolicy.rbacCould" -}}
(!{{ include "nodewright.packagePolicy.refused" . }} && variables.allow.exists(r, r.rbac && {{ include "nodewright.packagePolicy.ruleAllows" . }}))
{{- end -}}

{{/*
CEL string describing change item i for denial messages, e.g.
"Add drv=nvcr.io/nvidia/gpu-operator/driver:25.3.0".
*/}}
{{- define "nodewright.packagePolicy.describe" -}}
string(i.op) + ' ' + string(i.name) + '=' + string(i.image) + (i.version != '' ? ':' + string(i.version) : '') + (i.sha != '' ? '@' + string(i.sha) : '')
{{- end -}}
