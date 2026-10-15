from contextvars import ContextVar
from dataclasses import dataclass


@dataclass(frozen=True)
class Identity:
    user_id: str
    email: str
    display_name: str
    tenant_id: int
    role: str
    workbench_id: str = ""
    knowledge_base_id: str = ""
    knowledge_base_tenant_id: int = 0

    @property
    def can_write(self):
        return self.role in {"owner", "admin", "contributor"}

    def session_info(self):
        return {"weknora_user_id": self.user_id, "weknora_email": self.email,
                "weknora_display_name": self.display_name, "tenant_id": self.tenant_id}


current_identity: ContextVar[Identity | None] = ContextVar("research_identity", default=None)


def research_prefix():
    prefix = research_static_prefix()
    from tin.integration.workbench_scope import current_workbench
    scope = current_workbench.get()
    return prefix + "/workbenches/" + scope.workbench_id if scope else prefix


def research_static_prefix():
    from tin.config import settings
    return settings.public_prefix.rstrip("/") if settings.weknora_api_url else ""


def research_url(path):
    prefix = research_prefix()
    if not path.startswith("/") or path.startswith("//"):
        return path
    if prefix and (path == prefix or path.startswith(prefix + "/")):
        return path
    static = research_static_prefix()
    if prefix != static and static and path.startswith(static + "/"):
        path = path[len(static):]
        if path.startswith("/workbenches/"):
            raise PermissionError("Cannot navigate into another workbench scope")
    return prefix + path


def verified_actor(fallback=""):
    identity = current_identity.get()
    return identity.user_id if identity else fallback


def research_form_url(path):
    from urllib.parse import urlencode
    url = research_url(path)
    identity = current_identity.get()
    if identity is None:
        return url
    separator = "&" if "?" in url else "?"
    return url + separator + urlencode({"_research_tenant": identity.tenant_id, "_research_user": identity.user_id})
