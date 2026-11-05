{{- define "infra.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gophprofile
{{- end }}

{{/*
Лейблы компонента: include "infra.componentLabels" (dict "ctx" $ "name" "postgresql" "component" "postgresql").
По app.kubernetes.io/component на зависимость ссылаются сетевые политики
чарта gophprofile.
*/}}
{{- define "infra.componentLabels" -}}
{{ include "infra.labels" .ctx }}
{{ include "infra.selectorLabels" . }}
{{- end }}

{{- define "infra.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "infra.secretName" -}}
{{- printf "%s-credentials" .Release.Name }}
{{- end }}

{{/*
securityContext пода для Pod Security «restricted»: include "infra.podSecurityContext" (dict "uid" 70 "gid" 70).
*/}}
{{- define "infra.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .gid }}
fsGroup: {{ .gid }}
fsGroupChangePolicy: OnRootMismatch
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "infra.containerSecurityContext" -}}
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
{{- end }}

{{/*
Том данных StatefulSet: PVC или emptyDir. Вызов в volumeClaimTemplates и volumes.
*/}}
{{- define "infra.volumeClaimTemplates" -}}
{{- if .ctx.Values.persistence.enabled }}
volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      {{- with .ctx.Values.persistence.storageClass }}
      storageClassName: {{ . }}
      {{- end }}
      resources:
        requests:
          storage: {{ .size }}
{{- end }}
{{- end }}

{{- define "infra.dataVolume" -}}
{{- if not .Values.persistence.enabled }}
- name: data
  emptyDir: {}
{{- end }}
{{- end }}

{{- define "infra.monitoringAvailable" -}}
{{- if and .Values.monitoring.enabled (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") }}true{{ end }}
{{- end }}

{{/*
NetworkPolicy: DNS-egress и peer «поды того же компонента».
*/}}
{{- define "infra.np.dnsEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .namespace }}
      podSelector:
        matchLabels:
          {{- toYaml .podLabels | nindent 10 }}
  ports:
    - port: 53
      protocol: UDP
    - port: 53
      protocol: TCP
{{- end }}

{{- define "infra.np.fromMonitoring" -}}
- from:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .ns }}
  ports:
    - port: {{ .port }}
      protocol: TCP
{{- end }}

{{- define "infra.np.fromApp" -}}
- from:
    - podSelector:
        matchLabels:
          {{- toYaml .ctx.Values.appSelector | nindent 10 }}
  ports:
    - port: {{ .port }}
      protocol: TCP
{{- end }}
