/**
 * Analytics translations for Brazilian Portuguese locale.
 */
export default {
  analytics: {
    title: 'Análises',
    subtitle: 'Saúde da entrega e fluxo sem depender de iterações',
    loading: 'Carregando análises...',
    noData: 'Nenhum dado disponível',
    errorTitle: 'Não foi possível carregar as análises',
    unsupportedVersion:
      'O servidor retornou um formato de análise incompatível. Atualize após a implantação terminar.',
    collectionLoadError:
      'Não foi possível carregar as coleções. As análises mostram todos os itens do workspace.',
    retry: 'Tentar novamente',
    dateRange: 'Período',
    collection: 'Coleção',
    allItems: 'Todos os itens do workspace',
    from: 'De',
    to: 'Até',
    daysValue: '{value} d',
    items_one: '{count} item',
    items_other: '{count} itens',
    range: {
      last30Days: 'Últimos 30 dias',
      last12Weeks: 'Últimas 12 semanas',
      last6Months: 'Últimos 6 meses',
      lastYear: 'Último ano',
      custom: 'Personalizado',
    },
    validation: {
      invalid: 'Informe datas inicial e final válidas.',
      reversed: 'A data inicial deve ser anterior ou igual à data final.',
      too_long: 'Escolha um período de no máximo 366 dias.',
    },
    scope: {
      summary: '{items} itens atuais · {from}–{to}',
      currentWorkspace: 'Coorte atual do workspace',
      currentWorkspaceNote:
        'O período se aplica aos gráficos de fluxo e entrega; saúde e idade são retratos atuais. Os gráficos históricos usam os itens que estão neste workspace hoje. Itens movidos ou excluídos não são incluídos.',
      currentCollection: 'Coorte atual da coleção',
      currentCollectionNote:
        'O período se aplica aos gráficos de fluxo e entrega; saúde e idade são retratos atuais. Os gráficos históricos usam os itens que correspondem a esta coleção hoje. Alterar a coleção pode alterar a coorte.',
    },
    health: {
      title: 'Precisa de atenção',
      description: 'Trabalho atual não concluído com sinais que merecem revisão.',
      unfinished: 'Não concluídos',
      overdue: 'Atrasados',
      stale: 'Sem atividade',
      staleHint: 'Sem atividade há {days}+ dias',
      unassigned: 'Sem responsável',
      withoutPriority: 'Sem prioridade',
      withoutEstimate: 'Sem estimativa',
      attentionItems: 'Itens para revisar',
      item: 'Item',
      status: 'Status',
      age: 'Idade',
      signals: 'Sinais',
      flags: {
        overdue: 'Atrasado',
        stale: 'Sem atividade',
        unassigned: 'Sem responsável',
        without_priority: 'Sem prioridade',
        without_estimate: 'Sem estimativa',
      },
      allClear: 'Nenhum item não concluído corresponde a um sinal de atenção.',
    },
    throughput: {
      title: 'Criados vs. concluídos',
      description:
        'Entradas semanais e primeiras conclusões. Reabrir um item não reescreve sua conclusão original.',
      created: 'Criados',
      completed: 'Concluídos',
      net: 'Variação líquida',
      average: 'Média concluída / semana',
      period: 'Período',
      definition: 'Conclusão significa a primeira transição para um status concluído.',
    },
    aging: {
      title: 'Idade do trabalho em andamento',
      description: 'Há quanto tempo os itens atualmente não concluídos estão abertos.',
      total: 'Itens ativos',
      median: 'Idade mediana',
      p85: 'Percentil 85',
      ageBand: 'Faixa de idade',
      itemCount: 'Itens',
      byStatus: 'Idade por status',
      oldest: 'Itens não concluídos mais antigos',
      status: 'Status',
      noActive: 'Não há trabalho não concluído neste escopo.',
      buckets: {
        '0_7': '0–7 dias',
        '8_14': '8–14 dias',
        '15_30': '15–30 dias',
        '31_60': '31–60 dias',
        '61_plus': '61+ dias',
      },
    },
    deliveryTime: {
      title: 'Tempo de entrega',
      description: 'Da criação até a primeira conclusão, agrupado por semana de conclusão.',
      analyzed: 'Itens concluídos',
      average: 'Média',
      median: 'Mediana',
      p85: 'Percentil 85',
      period: 'Período de conclusão',
      completed: 'Concluídos',
      slowest: 'Maiores tempos de entrega',
      completedDate: 'Primeira conclusão',
      duration: 'Tempo de entrega',
      missingHistory_one:
        '1 item atualmente concluído foi excluído porque o histórico de conclusão está ausente.',
      missingHistory_other:
        '{count} itens atualmente concluídos foram excluídos porque o histórico de conclusão está ausente.',
      definition:
        'Medido da criação do item até a primeira transição para um status concluído. Reaberturas posteriores não alteram o valor.',
    },
    dataTable: {
      show: 'Ver tabela de dados',
    },
    insufficientData: {
      no_items: 'Este escopo ainda não tem itens.',
      no_active_items: 'Não há trabalho não concluído neste escopo.',
      no_completed_items: 'Nenhuma primeira conclusão foi registrada no período selecionado.',
      few_completed_items:
        'Apenas alguns itens foram concluídos neste período. Trate os percentis como uma indicação.',
    },
  },
};
