/**
 * Base class providing the drag-state fields and methods shared by the
 * form builder and screen editor stores.
 *
 * Replaces the former runtime mixin (`applyDragStateMixin`) with statically
 * visible composition: concrete stores extend this class and inherit the
 * identical state and methods instead of redeclaring them and patching
 * prototypes at runtime.
 */
export class DragStateStore {
  // === Drag State ===
  draggedField = $state(null);
  fieldDragState = $state(new Map());

  setDragState(fieldId, state) {
    this.fieldDragState.set(fieldId, state);
    this.fieldDragState = new Map(this.fieldDragState);
  }

  clearDragState() {
    this.fieldDragState.forEach((_, id) => {
      this.fieldDragState.set(id, { closestEdge: null });
    });
    this.fieldDragState = new Map(this.fieldDragState);
  }

  setDraggedField(field) {
    this.draggedField = field;
  }

  clearDraggedField() {
    this.draggedField = null;
  }

  resetDragState() {
    this.draggedField = null;
    this.fieldDragState = new Map();
  }
}
