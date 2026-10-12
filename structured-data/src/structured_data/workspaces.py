"""Verified request scopes; the existing pilot directory keeps its identity."""
from contextvars import ContextVar
from dataclasses import dataclass
from pathlib import Path
import threading


@dataclass(frozen=True)
class Identity:
    user_id: str
    email: str
    display_name: str
    tenant_id: int
    role: str

    @property
    def can_write(self):
        return self.role in {"owner", "admin", "contributor"}


current_identity = ContextVar("structured_workspace", default=None)


class WorkspaceRegistry:
    def __init__(self, root, pilot):
        self.root, self.pilot = Path(root), int(pilot)
        self._objects, self._lock = {}, threading.RLock()

    def directory(self, tenant=None):
        if tenant is None:
            identity = current_identity.get()
            if identity is None:
                raise PermissionError("Verified workspace identity is required")
            tenant = identity.tenant_id
        if type(tenant) is not int or tenant <= 0:
            raise ValueError("Invalid workspace")
        # Existing snapshots remain in place; new spaces cannot see that root.
        return self.root if tenant == self.pilot else self.root / "workspaces" / str(tenant)

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
