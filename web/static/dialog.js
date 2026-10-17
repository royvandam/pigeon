(function () {
  const fileFields = action => data => ({action, path: data.path, dir: data.dir || ''});
  const userFields = action => data => ({action, user_id: data.userId});
  const actions = new Map([
    ['rename', {
      title: 'Rename', endpoint: '/files', fields: fileFields('rename'),
      input: {name: 'name', label: 'New name', required: true, maxLength: 255, value: data => data.name}
    }],
    ['move', {
      title: 'Move', endpoint: '/files', fields: fileFields('move'),
      input: {name: 'destination', label: 'Destination folder', maxLength: 4095, value: data => data.dir || '', help: true}
    }],
    ['delete-file', {
      title: 'Delete', endpoint: '/files', fields: fileFields('delete'), confirm: true,
      submit: 'Delete', icon: 'fa-trash-can', danger: true,
      description: data => data.directory === 'true'
        ? 'This permanently deletes the folder and everything inside it.' : 'This permanently deletes the file.'
    }],
    ['delete-user', {
      title: 'Delete user', endpoint: '/admin', fields: userFields('delete'), confirm: true,
      submit: 'Delete', icon: 'fa-trash-can', danger: true,
      description: () => 'The user will lose access. Their uploads will be preserved and their username and folder will remain reserved.'
    }],
    ['reset-password', {
      title: 'Reset password for', endpoint: '/admin', fields: userFields('reset'), confirm: true,
      submit: 'Reset password', icon: 'fa-key', danger: true,
      description: data => 'This replaces the current password with a new four-word passphrase. '
        + (data.self === 'true' ? 'You will be signed out on all devices. ' : 'This user will be signed out on all devices. ')
        + 'Copy the new passphrase from the result page.'
    }],
    ['recycle-user', {
      title: 'Recycle account', endpoint: '/admin', fields: userFields('recycle'), confirm: true,
      submit: 'Recycle account', icon: 'fa-recycle',
      description: data => 'A new passphrase will be generated. This account will regain access to its preserved uploads with its existing role (' + data.role + ') and quota (' + data.quota + '). Previous passwords, sessions, and share links will remain revoked.'
    }],
    ['purge-user', {
      title: 'Purge account', endpoint: '/admin', fields: userFields('purge'), confirm: true,
      submit: 'Purge account', icon: 'fa-trash-can', danger: true,
      description: () => 'This permanently deletes the account and all its uploaded files and folders. The username and upload folder will become available for a new account. This cannot be undone.'
    }],
    ['share', {
      title: 'Share', endpoint: '/shares', fields: () => ({}), share: true,
      description: data => 'Anyone with the link can download ' + (data.directory === 'true'
        ? 'this folder as a ZIP, including files added later.' : 'this file.') + ' Links stay active until revoked or the item is renamed, moved, or deleted.'
    }]
  ]);

  // One controller applies every action's full state, including when the file
  // list is refreshed or the dialog switches from sharing to an edit/confirmation.
  class ActionDialog {
    constructor() {
      this.dialog = document.getElementById('actionDialog');
      this.form = document.getElementById('actionDialogForm');
      this.fields = document.getElementById('actionDialogFields');
      this.input = document.getElementById('actionDialogValue');
      this.cancel = document.getElementById('actionDialogCancel');
      this.confirm = document.getElementById('actionDialogConfirm');
      this.sharePanel = document.getElementById('shareControls');
      document.addEventListener('click', event => {
        const button = event.target.closest('button[data-dialog-action]');
        const action = button && actions.get(button.dataset.dialogAction);
        if (!action) return;
        event.preventDefault();
        this.open(action, button.dataset);
      });
      this.form.addEventListener('submit', event => {
        if (!this.sharePanel.hidden) event.preventDefault();
      });
      this.cancel.addEventListener('click', () => this.dialog.close());
    }

    open(action, data) {
      document.getElementById('actionDialogTitle').textContent = action.title + ' "' + data.name + '"' + (action.confirm ? '?' : '');
      const description = document.getElementById('actionDialogDescription');
      description.textContent = action.description ? action.description(data) : '';
      description.hidden = !action.description;
      // The hidden field named "action" shadows the form's action property.
      this.form.setAttribute('action', action.endpoint);
      this.fields.replaceChildren();
      for (const [name, value] of Object.entries(action.fields(data))) {
        const field = document.createElement('input');
        field.type = 'hidden';
        field.name = name;
        field.value = value;
        this.fields.append(field);
      }

      const edit = action.input;
      document.getElementById('actionDialogInputGroup').hidden = !edit;
      document.getElementById('actionDialogLabel').textContent = edit?.label || 'Destination folder';
      document.getElementById('actionDialogHelp').hidden = !edit?.help;
      if (edit?.help) this.input.setAttribute('aria-describedby', 'actionDialogHelp');
      else this.input.removeAttribute('aria-describedby');
      this.input.disabled = !edit;
      this.input.required = Boolean(edit?.required);
      this.input.name = edit?.name || 'destination';
      this.input.maxLength = edit?.maxLength || 4095;
      this.input.value = edit ? edit.value(data) : '';
      this.input.dispatchEvent(new Event('input', {bubbles: true}));

      document.getElementById('actionDialogConfirmLabel').textContent = action.submit || 'Save';
      document.getElementById('actionDialogConfirmIcon').className = 'fa-solid ' + (action.icon || 'fa-check') + ' mr-1';
      this.confirm.dataset.danger = String(Boolean(action.danger));
      this.confirm.hidden = this.confirm.disabled = Boolean(action.share);
      this.sharePanel.hidden = !action.share;
      document.getElementById('actionDialogCancelLabel').textContent = action.share ? 'Close' : 'Cancel';
      this.dialog.showModal();
      if (action.share) this.dialog.dispatchEvent(new CustomEvent('share', {detail: {path: data.path}}));
      if (edit) {
        this.input.focus();
        this.input.select();
      } else {
        this.cancel.focus();
      }
    }
  }

  new ActionDialog();
})();
