{{/*
Common labels
*/}}
{{- define "ainsel.labels" -}}
app.kubernetes.io/part-of: ainsel
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end }}

{{/*
Component labels
*/}}
{{- define "ainsel.componentLabels" -}}
{{ include "ainsel.labels" . }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Selector labels for a component
*/}}
{{- define "ainsel.selectorLabels" -}}
app.kubernetes.io/name: ainsel-{{ .component }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
*/}}

{{/*
Local mode: nothing is externally exposed — no hub ingress, no frontend
ingress (or frontend disabled), no MCP ingress (or MCP disabled), and no
OIDC issuer configured. In that situation the hub may safely run without
auth middleware; the chart then auto-enables HUB_ALLOW_INSECURE_NO_AUTH so
a port-forward-only install works with zero auth configuration. Note:
WebhookConnector CRs still get their own ingress via the gateway operator —
keep local mode truly local and do not create connectors on an exposed
cluster while relying on it.
*/}}
{{- define "ainsel.localMode" -}}
{{- $uiExposed := and .Values.ui.enabled .Values.ui.ingress.enabled -}}
{{- $mcpExposed := and .Values.mcp.enabled .Values.mcp.ingress.enabled -}}
{{- if and (not .Values.auth.oidcIssuer) (not .Values.hub.ingress.enabled) (not $uiExposed) (not $mcpExposed) -}}
true
{{- end -}}
{{- end -}}

{{/*
Whether an ingress className refers to ingress-nginx. An empty className is
treated as nginx for backwards compatibility with the chart defaults. The
nginx-specific regex paths and rewrite annotations are only rendered when
this returns "true"; other controllers get literal Prefix paths instead.
Usage:
  {{- $isNginx := eq (include "ainsel.isNginx" .Values.hub.ingress.className) "true" -}}
*/}}
{{- define "ainsel.isNginx" -}}
{{- if has (trim (lower (toString (default "nginx" .)))) (list "nginx" "kubernetes.io/ingress-nginx") -}}true{{- end -}}
{{- end -}}

{{/*
Build the annotation map for an Ingress, keeping nginx.ingress.kubernetes.io/*
annotations only when the controller is ingress-nginx so other controllers
(Traefik, HAProxy, cloud LBs) never receive rules they cannot interpret.
"nginxOnly" entries are the chart's own hardcoded nginx annotations (added
only for nginx); "extra" entries come from user values and are filtered the
same way.
Usage:
  {{- $annotations := fromYaml (include "ainsel.ingressAnnotations" (dict "isNginx" $isNginx "nginxOnly" (dict "nginx.ingress.kubernetes.io/use-regex" "true") "extra" .Values.hub.ingress.annotations)) -}}
*/}}
{{- define "ainsel.ingressAnnotations" -}}
{{- $isNginx := .isNginx -}}
{{- $annotations := dict -}}
{{- if $isNginx -}}
{{- range $key, $value := (default (dict) .nginxOnly) -}}
{{- $_ := set $annotations $key $value -}}
{{- end -}}
{{- end -}}
{{- range $key, $value := (default (dict) .extra) -}}
{{- if or $isNginx (not (hasPrefix "nginx.ingress.kubernetes.io/" $key)) -}}
{{- $_ := set $annotations $key $value -}}
{{- end -}}
{{- end -}}
{{- toYaml $annotations -}}
{{- end -}}

{{/* Roles claim; empty lets the services default to `groups`. */}}
{{- define "ainsel.oidcRolesClaim" -}}
{{- if .Values.auth.oidcRolesClaim -}}
{{- .Values.auth.oidcRolesClaim -}}
{{- else if .Values.auth.oidcProjectId -}}
urn:zitadel:iam:org:project:roles
{{- end -}}
{{- end -}}

{{/* Shared provider scopes for browser and MCP clients. */}}
{{- define "ainsel.oidcScopes" -}}
{{- if .Values.auth.oidcScopes -}}
{{- .Values.auth.oidcScopes -}}
{{- else if .Values.auth.oidcProjectId -}}
openid profile email urn:zitadel:iam:org:project:id:{{ .Values.auth.oidcProjectId }}:aud
{{- else -}}
openid profile email offline_access
{{- end -}}
{{- end -}}
