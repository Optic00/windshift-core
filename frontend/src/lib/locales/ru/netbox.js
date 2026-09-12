export default {
  netbox: {
    tab: 'NetBox', connections: 'Подключения NetBox', connectionsDescription: 'Связывайте рабочие элементы с выбранными устройствами и виртуальными машинами NetBox.',
    addConnection: 'Добавить подключение', editConnection: 'Изменить подключение', noConnections: 'Подключения NetBox не настроены', loadFailed: 'Не удалось загрузить подключения NetBox',
    name: 'Название', slug: 'Идентификатор', baseUrl: 'Базовый URL HTTPS', authScheme: 'Версия токена', bearer: 'Bearer-токен API v2', legacy: 'Устаревший API-токен', apiToken: 'API-токен', enabled: 'Включено',
    immutableHint: 'Базовый URL, идентификатор и версию токена изменить нельзя. Для их изменения создайте новое подключение.', tokenPreserve: 'Оставьте поле пустым, чтобы сохранить текущий токен.',
    allWorkspaces: 'Разрешить все рабочие области', allowedWorkspaces: 'Разрешённые рабочие области', selectWorkspace: 'Выберите хотя бы одну рабочую область.',
    sharingNotice: 'Данные, доступные этой служебной учётной записи NetBox, видны всем читателям элементов Windshift в разрешённых рабочих областях. Используйте токен только для чтения с минимальными правами.',
    save: 'Сохранить подключение', created: 'Подключение NetBox создано', updated: 'Подключение NetBox обновлено', saveFailed: 'Не удалось сохранить подключение NetBox',
    test: 'Проверить подключение', testSucceeded: 'Подключение NetBox работает', testFailed: 'Проверка подключения NetBox не удалась', noAllowedWorkspace: 'Для подключения не разрешена ни одна рабочая область. Выберите рабочую область перед проверкой доступа к объектам.',
    enableBeforeTesting: 'Включите подключение перед проверкой', editConflict: 'Это подключение изменилось во время редактирования. Отмените изменения и загрузите актуальные данные перед продолжением.', discardAndReload: 'Отменить изменения и загрузить заново',
    delete: 'Удалить подключение', deleteConfirm: 'Удалить «{name}»? Локальные снимки и связи будут удалены. Объекты в NetBox не изменятся.', deleted: 'Подключение NetBox удалено', deleteFailed: 'Не удалось удалить подключение NetBox',
    panelTitle: 'NetBox', linkObject: 'Связать объект', noLinks: 'Объекты NetBox ещё не связаны', noAvailableConnections: 'Для этой рабочей области нет доступного подключения NetBox.', loadLinksFailed: 'Не удалось загрузить связи NetBox',
    connection: 'Подключение', type: 'Тип', devices: 'Устройства', virtualMachines: 'Виртуальные машины', search: 'Найти', searchPlaceholder: 'Название или запрос', searchHint: 'Введите не менее 2 символов.', noResults: 'Подходящие объекты не найдены.', previous: 'Назад', next: 'Далее', link: 'Связать', linkFailed: 'Не удалось связать объект NetBox',
    open: 'Открыть в NetBox', refresh: 'Обновить', unlink: 'Удалить связь', refreshFailed: 'Обновление не удалось. Предыдущий снимок сохранён.', unlinkFailed: 'Не удалось удалить связь NetBox', snapshotFrom: 'Снимок от {date}', status: 'Статус', site: 'Площадка', role: 'Роль', ipv4: 'IPv4', ipv6: 'IPv6', close: 'Закрыть'
  }
};
