---
title: Supported Versions of the Kubernetes Documentation
content_template: templates/concept
---

{{% capture overview %}}

This website contains documentation for the current version of Kubernetes
and the four previous versions of Kubernetes.

{{% /capture %}}

{{% capture body %}}

## Current version

The current version is
[{{< param "version" >}}](/).

## Previous versions

{% for v in page.versions %}
{% if v.version != page.version %}
* [{{ v.version }}]({{v.url}})
{% endif %}
{% endfor %}

{{% /capture %}}


