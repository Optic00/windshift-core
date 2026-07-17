/**
 * Analytics translations for Spanish locale.
 */
export default {
  analytics: {
    title: 'Analíticas',
    subtitle: 'Salud de entrega y flujo sin necesidad de iteraciones',
    loading: 'Cargando analíticas...',
    noData: 'No hay datos disponibles',
    errorTitle: 'No se pudieron cargar las analíticas',
    unsupportedVersion:
      'El servidor devolvió un formato de analíticas no compatible. Actualiza la página cuando termine el despliegue.',
    collectionLoadError:
      'No se pudieron cargar las colecciones. Las analíticas muestran todos los elementos del espacio de trabajo.',
    retry: 'Reintentar',
    dateRange: 'Rango de fechas',
    collection: 'Colección',
    allItems: 'Todos los elementos del espacio de trabajo',
    from: 'Desde',
    to: 'Hasta',
    daysValue: '{value} d',
    items_one: '{count} elemento',
    items_other: '{count} elementos',
    range: {
      last30Days: 'Últimos 30 días',
      last12Weeks: 'Últimas 12 semanas',
      last6Months: 'Últimos 6 meses',
      lastYear: 'Último año',
      custom: 'Personalizado',
    },
    validation: {
      invalid: 'Introduce fechas de inicio y fin válidas.',
      reversed: 'La fecha de inicio debe ser anterior o igual a la fecha de fin.',
      too_long: 'Elige un rango de 366 días o menos.',
    },
    scope: {
      summary: '{items} elementos actuales · {from}–{to}',
      currentWorkspace: 'Cohorte actual del espacio de trabajo',
      currentWorkspaceNote:
        'El rango de fechas se aplica a los gráficos de flujo y entrega; la salud y la antigüedad son instantáneas actuales. Los gráficos históricos usan los elementos que están hoy en este espacio de trabajo. No se incluyen elementos movidos o eliminados.',
      currentCollection: 'Cohorte actual de la colección',
      currentCollectionNote:
        'El rango de fechas se aplica a los gráficos de flujo y entrega; la salud y la antigüedad son instantáneas actuales. Los gráficos históricos usan los elementos que coinciden hoy con esta colección. Cambiar la colección puede cambiar la cohorte.',
    },
    health: {
      title: 'Requiere atención',
      description: 'Trabajo pendiente actual con señales que conviene revisar.',
      unfinished: 'Pendiente',
      overdue: 'Vencido',
      stale: 'Inactivo',
      staleHint: 'Sin actividad durante {days}+ días',
      unassigned: 'Sin asignar',
      withoutPriority: 'Sin prioridad',
      withoutEstimate: 'Sin estimación',
      attentionItems: 'Elementos para revisar',
      item: 'Elemento',
      status: 'Estado',
      age: 'Antigüedad',
      signals: 'Señales',
      flags: {
        overdue: 'Vencido',
        stale: 'Inactivo',
        unassigned: 'Sin asignar',
        without_priority: 'Sin prioridad',
        without_estimate: 'Sin estimación',
      },
      allClear: 'Ningún elemento pendiente coincide ahora con una señal de atención.',
    },
    throughput: {
      title: 'Creados frente a completados',
      description:
        'Entradas semanales y primeras finalizaciones. Reabrir un elemento no modifica su finalización original.',
      created: 'Creados',
      completed: 'Completados',
      net: 'Cambio neto',
      average: 'Prom. completados / semana',
      period: 'Periodo',
      definition: 'La finalización es la primera transición a un estado completado.',
    },
    aging: {
      title: 'Antigüedad del trabajo en curso',
      description: 'Cuánto tiempo llevan abiertos los elementos actualmente pendientes.',
      total: 'Elementos activos',
      median: 'Antigüedad mediana',
      p85: 'Percentil 85',
      ageBand: 'Rango de antigüedad',
      itemCount: 'Elementos',
      byStatus: 'Antigüedad por estado',
      oldest: 'Elementos pendientes más antiguos',
      status: 'Estado',
      noActive: 'No hay trabajo pendiente en este ámbito.',
      buckets: {
        '0_7': '0–7 días',
        '8_14': '8–14 días',
        '15_30': '15–30 días',
        '31_60': '31–60 días',
        '61_plus': '61+ días',
      },
    },
    deliveryTime: {
      title: 'Tiempo de entrega',
      description: 'Desde la creación hasta la primera finalización, agrupado por semana.',
      analyzed: 'Elementos completados',
      average: 'Promedio',
      median: 'Mediana',
      p85: 'Percentil 85',
      period: 'Periodo de finalización',
      completed: 'Completados',
      slowest: 'Tiempos de entrega más largos',
      completedDate: 'Primera finalización',
      duration: 'Tiempo de entrega',
      missingHistory_one:
        'Se excluyó 1 elemento actualmente completado porque falta su historial de finalización.',
      missingHistory_other:
        'Se excluyeron {count} elementos actualmente completados porque falta su historial de finalización.',
      definition:
        'Se mide desde la creación hasta la primera transición a un estado completado. Las reaperturas posteriores no cambian el valor.',
    },
    dataTable: {
      show: 'Ver tabla de datos',
    },
    insufficientData: {
      no_items: 'Este ámbito aún no contiene elementos.',
      no_active_items: 'No hay trabajo pendiente en este ámbito.',
      no_completed_items: 'No se registraron primeras finalizaciones en el rango seleccionado.',
      few_completed_items:
        'Solo se completaron unos pocos elementos en este rango. Interpreta los percentiles como una orientación.',
    },
  },
};
