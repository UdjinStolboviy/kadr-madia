import type { Messages } from "../core";

// Keys in this file must start with "audit." (checked by the unit tests).
export const en = {
  "audit.title": "Audit log",
  "audit.description": "Every administrative action, newest first.",
  "audit.col.when": "When",
  "audit.col.action": "Action",
  "audit.col.target": "Target",
  "audit.col.project": "Project",
  "audit.col.actor": "Actor",
  "audit.col.ip": "IP",
  "audit.hideDetails": "Hide details",
  "audit.showDetails": "Show details",
  "audit.emptyTitle": "No audit events yet",
  "audit.emptyDesc": "Actions such as sign-ins and configuration changes will appear here.",
  "audit.count": "{count, plural, one {# event} other {# events}}",
  "audit.loadMore": "Load more",
  "audit.end": "End of log",
};

export const uk: Messages<typeof en> = {
  "audit.title": "Журнал аудиту",
  "audit.description": "Усі адміністративні дії, спочатку найновіші.",
  "audit.col.when": "Коли",
  "audit.col.action": "Дія",
  "audit.col.target": "Ціль",
  "audit.col.project": "Проєкт",
  "audit.col.actor": "Виконавець",
  "audit.col.ip": "IP",
  "audit.hideDetails": "Сховати подробиці",
  "audit.showDetails": "Показати подробиці",
  "audit.emptyTitle": "Подій аудиту ще немає",
  "audit.emptyDesc": "Тут з'являтимуться дії, як-от входи та зміни конфігурації.",
  "audit.count": "{count, plural, one {# подія} few {# події} many {# подій} other {# події}}",
  "audit.loadMore": "Завантажити ще",
  "audit.end": "Кінець журналу",
};

export const es: Messages<typeof en> = {
  "audit.title": "Registro de auditoría",
  "audit.description": "Todas las acciones administrativas, las más recientes primero.",
  "audit.col.when": "Cuándo",
  "audit.col.action": "Acción",
  "audit.col.target": "Destino",
  "audit.col.project": "Proyecto",
  "audit.col.actor": "Autor",
  "audit.col.ip": "IP",
  "audit.hideDetails": "Ocultar detalles",
  "audit.showDetails": "Mostrar detalles",
  "audit.emptyTitle": "Aún no hay eventos de auditoría",
  "audit.emptyDesc": "Aquí aparecerán acciones como inicios de sesión y cambios de configuración.",
  "audit.count": "{count, plural, one {# evento} other {# eventos}}",
  "audit.loadMore": "Cargar más",
  "audit.end": "Fin del registro",
};
