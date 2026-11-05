{{/*
Имя чарта (app.kubernetes.io/name).
*/}}
{{- define "gophprofile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Полное имя ресурсов релиза. Если имя релиза уже содержит имя чарта
(релиз gophprofile), оно не дублируется.
*/}}
{{- define "gophprofile.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "gophprofile.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Лейблы, общие для всех ресурсов.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ include "gophprofile.chart" . }}
{{ include "gophprofile.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gophprofile
{{- end }}

{{/*
Селектор релиза: name + instance. Компонент добавляется отдельно.
*/}}
{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Лейблы компонента. Вызов: include "gophprofile.componentLabels" (dict "ctx" $ "component" "server").
app — имя сервиса как в ТЗ (avatar-service, avatar-worker): из него
ServiceMonitor берёт job, и запросы дашбордов и алертов этапа 2 работают без правок.
*/}}
{{- define "gophprofile.componentLabels" -}}
{{ include "gophprofile.labels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- with (include "gophprofile.appLabel" .component) }}
app: {{ . }}
{{- end }}
{{- end }}

{{- define "gophprofile.componentSelectorLabels" -}}
{{ include "gophprofile.selectorLabels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "gophprofile.appLabel" -}}
{{- if eq . "server" }}avatar-service{{ else if eq . "worker" }}avatar-worker{{ end }}
{{- end }}

{{- define "gophprofile.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gophprofile.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "gophprofile.configMapName" -}}
{{- printf "%s-config" (include "gophprofile.fullname" .) }}
{{- end }}

{{- define "gophprofile.secretName" -}}
{{- default (printf "%s-secrets" (include "gophprofile.fullname" .)) .Values.secret.existingSecret }}
{{- end }}

{{/*
Образ компонента: include "gophprofile.image" (dict "ctx" $ "image" .Values.server.image).
Тег по умолчанию — Chart.appVersion.
*/}}
{{- define "gophprofile.image" -}}
{{- printf "%s:%s" .image.repository (default .ctx.Chart.AppVersion .image.tag | toString) }}
{{- end }}

{{/*
GOMEMLIMIT ≈ 90% от limits.memory, чтобы GC успевал освободить память до
OOMKill. Принимает значение вида 512Mi или 1Gi, отдаёт MiB для Go.
*/}}
{{- define "gophprofile.gomemlimit" -}}
{{- $m := toString . }}
{{- $mib := 0 }}
{{- if hasSuffix "Gi" $m }}
{{- $mib = mul (trimSuffix "Gi" $m | atoi) 1024 }}
{{- else if hasSuffix "Mi" $m }}
{{- $mib = trimSuffix "Mi" $m | atoi }}
{{- else }}
{{- fail (printf "resources.limits.memory %q: укажите значение в Mi или Gi, от него считается GOMEMLIMIT" $m) }}
{{- end }}
{{- printf "%dMiB" (div (mul $mib 90) 100) }}
{{- end }}

{{/*
Проба HTTP: include "gophprofile.probe" (dict "probe" .Values.server.probes.liveness "port" "http").
*/}}
{{- define "gophprofile.probe" -}}
httpGet:
  path: {{ .probe.path }}
  port: {{ .port }}
periodSeconds: {{ .probe.periodSeconds }}
failureThreshold: {{ .probe.failureThreshold }}
timeoutSeconds: {{ .probe.timeoutSeconds | default 2 }}
{{- end }}

{{/*
Аннотации checksum: изменение ConfigMap или Secret (если его рендерит чарт)
перезапускает поды при helm upgrade.
*/}}
{{- define "gophprofile.checksums" -}}
checksum/config: {{ include (print .Template.BasePath "/configmap.yaml") . | sha256sum }}
{{- if not .Values.secret.existingSecret }}
checksum/secret: {{ include (print .Template.BasePath "/secret.yaml") . | sha256sum }}
{{- end }}
{{- end }}

{{/*
Общие env и тома подов server и worker.
*/}}
{{- define "gophprofile.envFrom" -}}
- configMapRef:
    name: {{ include "gophprofile.configMapName" . }}
- secretRef:
    name: {{ include "gophprofile.secretName" . }}
{{- end }}

{{- define "gophprofile.tmpVolume" -}}
- name: tmp
  emptyDir:
    sizeLimit: {{ .Values.tmpVolume.sizeLimit }}
{{- end }}

{{/*
Есть ли в кластере CRD Prometheus Operator. Без них ServiceMonitor,
PrometheusRule не рендерятся, и чарт ставится без ошибок.
*/}}
{{- define "gophprofile.monitoringAvailable" -}}
{{- if and .Values.monitoring.enabled (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") }}true{{ end }}
{{- end }}

{{/*
NetworkPolicy: egress в CoreDNS (вызов с .Values.networkPolicy.dns) и egress
к зависимости (вызов с элементом .Values.networkPolicy.egress).
*/}}
{{- define "gophprofile.np.dnsEgress" -}}
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
{{- define "gophprofile.np.egressTo" -}}
- to:
    {{- toYaml .to | nindent 4 }}
  ports:
    - port: {{ .port }}
      protocol: TCP
{{- end }}
