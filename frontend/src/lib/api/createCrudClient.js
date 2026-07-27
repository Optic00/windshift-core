import { fetchAPI } from './core.js';
import { buildQueryString } from './utils.js';

/** Build CRUD clients with parent-scoped, flat-item, or admin-write paths. */
export function createCrudClient(basePath, options = {}) {
  const { parentPath, itemPath, adminBasePath } = options;

  if (parentPath) {
    const collection = (parentId) => `${parentPath}/${parentId}${basePath}`;

    if (itemPath) {
      // List/create nest under parent; item operations stay flat.
      const item = (id) => `${itemPath}/${id}`;
      return {
        getAll: (parentId, filters = {}, requestOptions = {}) =>
          fetchAPI(`${collection(parentId)}${buildQueryString(filters)}`, requestOptions),
        get: (id, requestOptions = {}) => fetchAPI(item(id), requestOptions),
        create: (parentId, data) =>
          fetchAPI(collection(parentId), {
            method: 'POST',
            body: JSON.stringify(data),
          }),
        update: (id, data) =>
          fetchAPI(item(id), {
            method: 'PUT',
            body: JSON.stringify(data),
          }),
        delete: (id) =>
          fetchAPI(item(id), {
            method: 'DELETE',
          }),
      };
    }

    // Every operation nests under the parent.
    const item = (parentId, id) => `${collection(parentId)}/${id}`;
    return {
      getAll: (parentId, filters = {}, requestOptions = {}) =>
        fetchAPI(`${collection(parentId)}${buildQueryString(filters)}`, requestOptions),
      get: (parentId, id, requestOptions = {}) => fetchAPI(item(parentId, id), requestOptions),
      create: (parentId, data) =>
        fetchAPI(collection(parentId), {
          method: 'POST',
          body: JSON.stringify(data),
        }),
      update: (parentId, id, data) =>
        fetchAPI(item(parentId, id), {
          method: 'PUT',
          body: JSON.stringify(data),
        }),
      delete: (parentId, id) =>
        fetchAPI(item(parentId, id), {
          method: 'DELETE',
        }),
    };
  }

  const writePath = adminBasePath ?? basePath;
  return {
    getAll: (filters = {}, requestOptions = {}) =>
      fetchAPI(`${basePath}${buildQueryString(filters)}`, requestOptions),
    get: (id, requestOptions = {}) => fetchAPI(`${basePath}/${id}`, requestOptions),
    create: (data) =>
      fetchAPI(writePath, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    update: (id, data) =>
      fetchAPI(`${writePath}/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    delete: (id) =>
      fetchAPI(`${writePath}/${id}`, {
        method: 'DELETE',
      }),
  };
}
