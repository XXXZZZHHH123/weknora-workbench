"""Verified request scopes; the existing pilot directory keeps its identity."""
from contextvars import ContextVar
from dataclasses import dataclass
from pathlib import Path
import re
import threading


def valid_knowledge_base_id(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", value) is not None


@dataclass(frozen=True)
class Identity:
    user_id: str
    email: str
    display_name: str
    tenant_id: int
    role: str
    knowledge_base_id: str = ""
    knowledge_base_tenant_id: int | None = None
    knowledge_base_can_write: bool = False
    workbench_id: str = ''
    scope_version: int = 1
    sources_revision: int = 0

    @property
    def can_write(self):
        return self.role in {"owner", "admin", "contributor"} and (
            not self.knowledge_base_id or self.knowledge_base_can_write)


current_identity = ContextVar("structured_workspace", default=None)


class WorkspaceRegistry:
    def __init__(self, root, pilot):
        self.root, self.pilot = Path(root), int(pilot)
        self._objects, self._lock = {}, threading.RLock()

    def directory(self, tenant=None):
        knowledge_base_id = ""
        if tenant is None:
            identity = current_identity.get()
            if identity is None:
                raise PermissionError("Verified workspace identity is required")
            knowledge_base_id = identity.knowledge_base_id
            tenant = identity.knowledge_base_tenant_id if knowledge_base_id else identity.tenant_id
            if knowledge_base_id and not valid_knowledge_base_id(knowledge_base_id):
                raise ValueError("Invalid knowledge base")
        if type(tenant) is not int or tenant <= 0:
            raise ValueError("Invalid workspace")
        # Existing snapshots remain in place; new spaces cannot see that root.
        directory = self.root if tenant == self.pilot else self.root / "workspaces" / str(tenant)
        return directory / "knowledge-bases" / knowledge_base_id if knowledge_base_id else directory

    def objects(self):
        directory = self.directory()
        with self._lock:
            if directory not in self._objects:
                from .store import DataStore
                from .imports import ImportManager
                from .field_resolver import FieldResolver
                store = DataStore(directory)
                self._objects[directory] = {"store": store, "manager": ImportManager(directory),
                                            "resolver": FieldResolver(store), 'import_lock':threading.Lock()}
            return self._objects[directory]

    def proxy(self, name):
        registry = self
        class ScopedProxy:
            def __getattr__(self, attribute):
                return getattr(registry.objects()[name], attribute)
        return ScopedProxy()
